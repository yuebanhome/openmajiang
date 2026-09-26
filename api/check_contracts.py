#!/usr/bin/env python3
"""Offline schema checks plus positive/negative cross-language protocol fixtures."""
import copy
import json
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource

ROOT=Path(__file__).resolve().parent
documents={p.name:json.loads(p.read_text()) for p in (ROOT/"schemas").glob("*.json")}
registry=Registry().with_resources((doc["$id"],Resource.from_contents(doc)) for doc in documents.values())
for doc in documents.values():Draft202012Validator.check_schema(doc)
def validator(name):return Draft202012Validator(documents[name],registry=registry,format_checker=FormatChecker())
def rejects(check,value):
 if check.is_valid(value):raise AssertionError("invalid fixture was accepted")

fixture=json.loads((ROOT/"fixtures/decision.json").read_text())
server,client,spectator=validator("ws-server.json"),validator("ws-client.json"),validator("spectator.json")
server.validate(fixture["snapshot"]);server.validate(fixture["decision"])
action={k:fixture["decision"][k] for k in ["match_id","hand_id","participant_id","seat_id","seat_assignment_version","control_epoch","decision_id","window_id"]}
action.update(type="submit_action",protocol_version="1.0",command_id="fixture-command-001",option_id="discard_1")
client.validate(action)
rejects(client,{**action,"hand":["secret"]})
rejects(client,{**action,"command_id":"tiny"})
rejects(server,{**fixture["decision"],"observation_ref":{"stream_id":"s"}})
public=json.loads((ROOT/"fixtures/spectator.json").read_text())
spectator.validate(public);server.validate(public)
for key in ["hand","flowers","melds","winning_tile","winning_hand","seed","wall","fan_items","decomposition"]:
 leaked=copy.deepcopy(public);leaked["view"][key]=[];rejects(spectator,leaked)
leaked=copy.deepcopy(public);leaked["view"]["discards"][0]["tile_id"]="physical-entity";rejects(spectator,leaked)
spec=json.loads((ROOT/"openapi.json").read_text())
assert spec["openapi"]=="3.1.0" and spec["paths"]
local_docs={(ROOT/"openapi.json").resolve():spec,**{(ROOT/"schemas"/name).resolve():doc for name,doc in documents.items()}}
def resolve_refs(value,base):
 if isinstance(value,dict):
  if "$ref" in value:
   filename,_,pointer=value["$ref"].partition("#")
   target=local_docs[(base.parent/filename).resolve()] if filename else local_docs[base]
   for part in pointer.lstrip("/").split("/") if pointer else []:
    target=target[part.replace("~1","/").replace("~0","~")]
  for child in value.values():resolve_refs(child,base)
 elif isinstance(value,list):
  for child in value:resolve_refs(child,base)
for base,doc in local_docs.items():resolve_refs(doc,base)
for path,item in spec["paths"].items():
 for method,operation in item.items():
  assert operation["operationId"] and operation["responses"],(method,path)
  if "requestBody" in operation:
   assert "$ref" in operation["requestBody"]["content"]["application/json"]["schema"]
print(f"Validated {len(documents)} schemas, 2 shared private frames, discard-only fixture and negative privacy/action cases")
