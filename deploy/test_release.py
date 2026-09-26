import unittest
import urllib.error
from unittest.mock import patch
from release import Registry, image_name, version


class ReleaseVersionTests(unittest.TestCase):
    def test_release_and_prerelease(self):
        for value in ["0.1.0", "1.0.0", "12.3.45", "1.0.0-rc.1", "1.0.0-0", "1.0.0-alpha-1"]:
            self.assertEqual(version(value), value)

    def test_unsafe_or_noncanonical_versions(self):
        for value in ["v1.0.0", "01.0.0", "1.0.0-01", "1.0", "1.0.0+build", "1.0.0\n", "1.0.0;echo bad", "1.0.0/evil", "", "latest"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                version(value)

    def test_latest_excludes_prerelease(self):
        self.assertEqual(version("1.0.0", stable=True), "1.0.0")
        with self.assertRaises(ValueError):
            version("1.0.0-rc.1", stable=True)

    def test_registry_only_accepts_not_found_as_absent(self):
        registry = object.__new__(Registry)
        with patch.object(registry, "get", side_effect=urllib.error.HTTPError("url", 404, "not found", {}, None)):
            registry.assert_absent("1.0.0")
        for code in [401, 403, 429, 500, 503]:
            with patch.object(registry, "get", side_effect=urllib.error.HTTPError("url", code, "failure", {}, None)):
                with self.subTest(status=code), self.assertRaises(urllib.error.HTTPError):
                    registry.assert_absent("1.0.0")
        with patch.object(registry, "get", return_value=({}, "sha256:existing")):
            with self.assertRaises(ValueError):
                registry.assert_absent("1.0.0")

    def test_image_cannot_escape_configured_repository(self):
        with patch.dict("os.environ", {"DOCKERHUB_USER": "alice", "DOCKERHUB_NAMESPACE": ""}):
            self.assertEqual(image_name(), "docker.io/alice/openmajiang")
        for namespace in ["alice/elsewhere", "other.registry/repo", "alice:tag", "alice\nmalicious"]:
            with patch.dict("os.environ", {"DOCKERHUB_NAMESPACE": namespace}):
                with self.subTest(namespace=namespace), self.assertRaises(ValueError):
                    image_name()


if __name__ == "__main__":
    unittest.main()
