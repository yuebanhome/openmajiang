#!/usr/bin/env python3
"""Validate release metadata and inspect Docker Hub without leaking credentials."""
import argparse
import base64
import hashlib
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

SEMVER = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
ACCEPT = ", ".join([
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
])


def version(value, stable=False):
    match = SEMVER.fullmatch(value)
    if not match or len(value) > 128:
        raise ValueError("Expected a Docker-compatible SemVer (no v prefix or build metadata)")
    prerelease = match.group(4)
    if prerelease:
        if stable:
            raise ValueError("A prerelease cannot be promoted to latest")
        if any(part.isdigit() and len(part) > 1 and part.startswith("0") for part in prerelease.split(".")):
            raise ValueError("Numeric prerelease identifiers must not contain leading zeroes")
    return value


def image_name():
    namespace = os.environ.get("DOCKERHUB_NAMESPACE") or os.environ.get("DOCKERHUB_USER", "")
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{1,254}", namespace):
        raise ValueError("Docker Hub namespace must be a lowercase account or organization name")
    return f"docker.io/{namespace}/openmajiang"


def git(*args):
    return subprocess.check_output(["git", *args], text=True).strip()


def check_tag(value, stable=False):
    version(value, stable)
    ref = "refs/tags/v" + value
    commit = git("rev-parse", ref + "^{commit}")
    subprocess.run(["git", "merge-base", "--is-ancestor", commit, "origin/main"], check=True)
    if git("show", f"{commit}:VERSION") != value:
        raise ValueError("Tag version does not match the VERSION file at its commit")
    return commit


class Registry:
    def __init__(self, image):
        if not image.startswith("docker.io/"):
            raise ValueError("Only the configured Docker Hub repository is accepted")
        self.repository = image.removeprefix("docker.io/")
        params = urllib.parse.urlencode({"service": "registry.docker.io", "scope": f"repository:{self.repository}:pull,push"})
        credentials = (os.environ["DOCKERHUB_USER"] + ":" + os.environ["DOCKERHUB_TOKEN"]).encode()
        request = urllib.request.Request("https://auth.docker.io/token?" + params)
        request.add_header("Authorization", "Basic " + base64.b64encode(credentials).decode())
        with urllib.request.urlopen(request, timeout=30) as response:
            self.token = json.load(response)["token"]

    def get(self, kind, ref):
        request = urllib.request.Request(f"https://registry-1.docker.io/v2/{self.repository}/{kind}/{ref}")
        request.add_header("Authorization", "Bearer " + self.token)
        request.add_header("Accept", ACCEPT)
        with urllib.request.urlopen(request, timeout=30) as response:
            data = response.read()
            digest = "sha256:" + hashlib.sha256(data).hexdigest()
            claimed = response.headers.get("Docker-Content-Digest")
            if claimed and claimed != digest:
                raise ValueError("Registry digest does not match manifest bytes")
            if ref.startswith("sha256:") and digest != ref:
                raise ValueError("Registry returned unexpected digest")
            return json.loads(data), digest

    def assert_absent(self, ref):
        try:
            self.get("manifests", ref)
        except urllib.error.HTTPError as error:
            if error.code == 404:
                return
            raise
        raise ValueError(f"Immutable image tag already exists: {ref}")

    def verify_release(self, value, expected_digest, commit):
        if not DIGEST.fullmatch(expected_digest):
            raise ValueError("Expected an exact sha256 digest")
        index, actual_digest = self.get("manifests", value)
        if actual_digest != expected_digest:
            raise ValueError("Supplied digest does not match the published version")
        manifests = index.get("manifests")
        if not manifests:
            raise ValueError("A release must be a multi-architecture image index")
        platforms = set()
        for item in manifests:
            platform = item.get("platform", {})
            arch = platform.get("architecture")
            if platform.get("os") != "linux" or arch not in {"amd64", "arm64"}:
                continue  # BuildKit provenance/SBOM descriptors are not runnable images.
            manifest, _ = self.get("manifests", item["digest"])
            config, _ = self.get("blobs", manifest["config"]["digest"])
            labels = config.get("config", {}).get("Labels", {})
            expected = {
                "org.opencontainers.image.revision": commit,
                "org.opencontainers.image.version": value,
                "org.opencontainers.image.source": "https://github.com/" + os.environ["GITHUB_REPOSITORY"],
            }
            if any(labels.get(key) != val for key, val in expected.items()):
                raise ValueError(f"{arch} image metadata does not match the verified release commit")
            platforms.add(arch)
        if platforms != {"amd64", "arm64"}:
            raise ValueError("Release is missing a tested architecture")


def output(key, value):
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as handle:
        handle.write(f"{key}={value}\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["metadata", "absent", "promote"])
    args = parser.parse_args()
    image = image_name()
    if args.command == "metadata":
        ref = os.environ["GITHUB_REF"]
        commit = os.environ["GITHUB_SHA"]
        release_version = "main"
        if ref.startswith("refs/tags/v"):
            release_version = version(ref.removeprefix("refs/tags/v"))
            if check_tag(release_version) != commit:
                raise ValueError("Workflow checkout does not match release tag commit")
            tags = f"{image}:{release_version}"
        elif ref == "refs/heads/main":
            if git("rev-parse", "origin/main") != commit:
                raise ValueError("This main commit has been superseded; do not move the main image backwards")
            tags = f"{image}:sha-{commit},{image}:main"
        else:
            raise ValueError("Only main and validated release tags can publish")
        output("image", image)
        output("version", release_version)
        output("tags", tags)
    elif args.command == "absent":
        value = os.environ["RELEASE_VERSION"]
        if value == "main":
            commit = os.environ["GITHUB_SHA"]
            if not re.fullmatch(r"[0-9a-f]{40}", commit):
                raise ValueError("Invalid source commit")
            ref = "sha-" + commit
        else:
            ref = version(value)
        Registry(image).assert_absent(ref)
    else:
        if os.environ["GITHUB_REF"] != "refs/heads/main":
            raise ValueError("Promotion must execute the workflow from main")
        value = version(os.environ["RELEASE_VERSION"], stable=True)
        commit = check_tag(value, stable=True)
        # A successful run of this exact CI workflow at the release commit is
        # required, independently of user-controlled OCI annotations.
        path = f"repos/{os.environ['GITHUB_REPOSITORY']}/actions/workflows/ci.yml/runs?event=push&head_sha={commit}&status=success&per_page=100"
        runs = json.loads(subprocess.check_output(["gh", "api", path], text=True))["workflow_runs"]
        if not any(run.get("head_branch") == "v" + value and run.get("conclusion") == "success" for run in runs):
            raise ValueError("No successful tag release run exists for this commit and version")
        digest = os.environ["RELEASE_DIGEST"]
        Registry(image).verify_release(value, digest, commit)
        output("image", image)
        output("digest", digest)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, subprocess.CalledProcessError, urllib.error.URLError) as error:
        # Do not include request headers, authorization values or environment.
        print(f"Release validation failed: {error}", file=sys.stderr)
        sys.exit(1)
