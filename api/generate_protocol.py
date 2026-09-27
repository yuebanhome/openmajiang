#!/usr/bin/env python3
"""Build discoverable contracts. Public DTOs come from the Go schema generator.
Closed protocol envelopes are explicit here; route coverage is checked against Go.
"""
import json
from pathlib import Path
import re

ROOT=Path(__file__).resolve().parents[1]
BASE="https://openmajiang.invalid/schemas/"
S={"type":"string"}; I={"type":"integer"}; B={"type":"boolean"}; T={"type":"string","format":"date-time"}
def obj(p,required=None):return {"type":"object","properties":p,"required":list(p) if required is None else required,"additionalProperties":False}
def arr(v):return {"type":"array","items":v}
def ref(n):return {"$ref":"#/$defs/"+n}
def dto(n):return {"$ref":"dto.json#/$defs/"+n}
def nullable(v):return {"anyOf":[v,{"type":"null"}]}
def const(v):return {"const":v}
def enum(*v):return {"type":"string","enum":list(v)}
def write(path,value):path.write_text(json.dumps(value,ensure_ascii=False,indent=2)+"\n")
D={}
def define(n,p,required=None):D[n]=obj(p,required);return ref(n)

define("Error",{"error":obj({"code":S,"message":S},["code"])})
define("Empty",{})
define("Ok",{"ok":B})
define("Accepted",{"message":S})
define("EmailRequest",{"email":{"type":"string","format":"email","maxLength":254}})
password={"type":"string","minLength":15,"maxLength":128,"writeOnly":True,"description":"Unicode characters; spaces are preserved. Never log request bodies."}
define("RegisterRequest",{"email":D["EmailRequest"]["properties"]["email"],"password":password,"name":{"type":"string","minLength":1,"maxLength":40},"accept_terms":const(True)})
define("LoginRequest",{"email":S,"password":{"type":"string","writeOnly":True}})
define("TokenRequest",{"token":{"type":"string","writeOnly":True}})
define("ResetRequest",{"token":{"type":"string","writeOnly":True},"password":password})
define("NameRequest",{"name":{"type":"string","minLength":1,"maxLength":40}})
define("PasswordRequest",{"password":{"type":"string","writeOnly":True}})
define("PasswordChange",{"current_password":{"type":"string","writeOnly":True},"new_password":password})
define("EmailChange",{"email":S,"password":{"type":"string","writeOnly":True}})
define("UserEnvelope",{"user":dto("User"),"csrf_token":S},["user"])
define("LoginResponse",{"user":dto("User"),"csrf_token":S})
define("Session",{"id":S,"user_agent":S,"created_at":T,"last_seen_at":T,"expires_at":T,"current":B})
define("SessionList",{"sessions":arr(ref("Session"))})
define("EmailDelivery",{"status":enum("none","queued","sending","sent","failed"),"attempts":I,"created_at":T,"next_attempt_at":T,"notice":S},["status"])
for name,key in [("Verified","verified"),("Reset","reset"),("LoggedOut","logged_out"),("Revoked","revoked"),("Deleted","deleted")]:define(name,{key:B})
define("Changed",{"changed":B,"login_required":B})
define("RuleIdentity",{"id":S,"version":S})
define("RoomEnvelope",{"room":dto("Room"),"own_participant_id":S},["room"])
define("RoomCreated",{"room":dto("Room"),"invite_code":{"type":"string","description":"Only issued to the owner. Never included in public room reads."}})
define("Rooms",{"rooms":arr(dto("Room"))})
define("CreateRoom",{"name":{"type":"string","maxLength":50},"mode":enum("human_only","mixed","bot_only"),"ruleset_id":S,"ruleset_version":S,"match_format":S,"online_profile":S,"seat_count":I,"invite_only":B,"self_test":B},[])
define("JoinRoom",{"invite_code":S},[])
define("InviteCode",{"invite_code":S})
define("Ready",{"ready":B})
D["AddBot"]={"oneOf":[obj({"builtin":enum("random_legal","basic_heuristic")}),obj({"bot_id":S})]}
define("LeftRoom",{"ok":B,"leave_after_hand":B})
define("QueueRequest",{"ruleset_id":S,"ruleset_version":S,"match_format":S,"continuous":B},[])
define("QueueStatus",{"status":enum("queued","cancelled")})
define("HumanQueue",{"queued":B,"room_id":S})
define("ActiveMatch",{"room":nullable(dto("Room")),"match":{ "type":"null"},"match_id":S},["room"])
define("CommandAck",{"type":const("command_ack"),"command_id":S,"decision_id":S,"status":enum("recorded","applied")})
define("ObservationRef",{"stream_id":S,"view_seq":{"type":"integer","minimum":1}})
identity={"match_id":S,"hand_id":S,"participant_id":S,"seat_id":I,"seat_assignment_version":I,"control_epoch":I}
define("Decision",{"type":const("decision_request"),"protocol_version":const("1.0"),**identity,"decision_id":S,"window_id":S,"phase":S,"server_time":T,"deadline_at":T,"legal_actions":arr(dto("Option")),"ruleset":ref("RuleIdentity"),"match_format":S,"observation_ref":ref("ObservationRef"),"stream_id":S,"view_seq":I},["type","protocol_version",*identity,"decision_id","window_id","phase","server_time","deadline_at","legal_actions","ruleset","match_format"])
common={"room":dto("Room"),"match_id":S,"hand_id":S,"hand_index":I,"status":S,"deadline_at":nullable(T),"platform_interrupted":B,"ruleset":ref("RuleIdentity"),"match_format":S,"seq":I,"stream_id":S,"view_seq":I}
define("ParticipantSnapshot",{"type":const("snapshot"),**common,**identity,"view":dto("MCRParticipantView"),"decision":ref("Decision"),"recorded":ref("CommandAck"),"self_timeout_count":I,"reaction_timeout_count":I,"control_token":S,"control_status":enum("owner","readonly")},["type","room",*identity,"hand_index","status","view","ruleset","match_format","platform_interrupted"])
define("SpectatorSnapshot",{"type":const("spectator_snapshot"),**common,"view_policy":const("spectator_discard_only@1"),"view":dto("MCRSpectatorView"),"match":obj({"id":S,"room_id":S,"status":S,"match_format":S})},["type","room","view","view_policy","match_id","hand_id","hand_index","status"])
define("RoomSnapshot",{"type":const("room_snapshot"),"room":dto("Room"),"view":{"type":"null"},"stream_id":S,"view_seq":I,"participant_id":S,"seat_id":I,"control_epoch":I,"control_token":S,"control_status":enum("owner","readonly")},["type","room","view"])
define("PublicRoomSnapshot",{"type":const("room_snapshot"),"room":dto("Room"),"view":{"type":"null"},"stream_id":S,"view_seq":I},["type","room","view"])
D["PrivateSnapshot"]={"oneOf":[ref("ParticipantSnapshot"),ref("RoomSnapshot")]}
D["PublicSnapshot"]={"oneOf":[ref("SpectatorSnapshot"),ref("PublicRoomSnapshot")]}
define("SpectatorTicket",{"ticket":{"type":"string","readOnly":True,"description":"Five-minute read-only spectator capability, used in first WS frame."},"expires_at":T,"view_policy":const("spectator_discard_only@1")})
for audience,name,view in [("participant_private","PrivateReplay","MCRParticipantView"),("spectator_discard_only@1","PublicReplay","MCRSpectatorView")]:
 define(name,{"frames":arr(obj({"seq":I,"hand_index":I,"hand_id":S,"view":dto(view),"at":T})),"next_after":I,"view_policy":const(audience)})
define("ArchivedScore",{"participant_id":S,"kind":enum("human","bot"),"total":I,"rank":I,"standard_points":obj({"numerator":I,"denominator":I})},["participant_id","kind","total"])
define("ArchiveSummary",{"version":const("settled-scores@1"),"completed_hands":I,"scores":arr(ref("ArchivedScore"))})
define("ArchivedMatch",{"archived":const(True),"archived_at":T,"summary":ref("ArchiveSummary"),"match":obj({"id":S,"room_id":S,"status":S,"match_format":S})})
D["PublicMatchInfo"]={"oneOf":[ref("PublicSnapshot"),ref("ArchivedMatch")]}
define("MatchSummary",{"id":S,"room_id":S,"status":S,"ruleset_id":S,"ruleset_version":S,"match_format":S,"mode":enum("human_only","mixed","bot_only"),"platform_interrupted":B,"created_at":T,"archived_at":nullable(T),"public_summary":{"oneOf":[ref("Empty"),ref("ArchiveSummary")]}})
define("Matches",{"matches":arr(ref("MatchSummary")),"next_before":S})
define("Bot",{"id":S,"name":S,"enabled":B,"current_version":S,"online":B,"suspended":B},["id","name","enabled","current_version"])
define("BotEnvelope",{"bot":ref("Bot")})
define("Bots",{"bots":arr(ref("Bot"))})
define("BotName",{"name":{"type":"string","minLength":1,"maxLength":50}})
define("BotPatch",{"name":{"type":"string","minLength":1,"maxLength":50},"enabled":B},[])
metadata={"description":"Arbitrary strategy-author metadata JSON, never executable code or credentials."}
define("BotVersionCreate",{"label":{"type":"string","minLength":1,"maxLength":80},"metadata":metadata},["label"])
define("BotVersion",{"id":S,"label":S,"metadata":metadata,"created_at":T},["id","label"])
define("VersionEnvelope",{"version":ref("BotVersion")})
define("Versions",{"versions":arr(ref("BotVersion"))})
define("Credential",{"id":S,"created_at":T,"revoked_at":nullable(T)})
define("Credentials",{"credentials":arr(ref("Credential"))})
define("CredentialCreated",{"credential_id":S,"api_key":{"type":"string","description":"Only returned once. Keep outside source, URL, CLI arguments, and logs."},"shown_once":const(True)})
define("BotSessionRequest",{"protocol_version":const("1.0"),"rulesets":arr(ref("RuleIdentity"))})
define("BotSessionResponse",{"session_token":S,"expires_at":T,"bot_id":S,"protocol_version":const("1.0")})
define("RuleList",{"rulesets":arr(dto("Manifest"))})
define("RuleEnvelope",{"ruleset":dto("Manifest")})
define("PublicStatus",{"maintenance":B,"message":S})
define("AdminStatus",{"maintenance":B,"message":S,"active_matches":I,"waiting_rooms":I,"queued":I,"mail_queued":I,"mail_failed":I})
reason={"type":"string","minLength":2,"maxLength":200}
define("Reason",{"reason":reason})
define("MaintenanceRequest",{"enabled":B,"message":{"type":"string","maxLength":160},"reason":reason},["enabled","reason"])
define("UserStatusRequest",{"status":enum("active","restricted"),"reason":reason})
define("RuleStatusRequest",{"enabled":B,"reason":reason})
define("MaintenanceResponse",{"maintenance":B})
define("RuleStatusResponse",{"enabled":B})
define("BotSuspended",{"ok":B,"suspended":B,"new_credential_required":B})
define("AuditEntry",{"id":I,"actor_id":S,"action":S,"target_id":S,"reason":S,"outcome":S,"created_at":T})
define("Audit",{"entries":arr(ref("AuditEntry")),"next_before":I})
define("AbortResponse",{"match_id":S,"status":const("aborted_by_operator")})
define("Health",{"status":enum("alive","ready")})
define("Metric",{"value":nullable({"type":"number"}),"samples":I})
dimensions={k:S for k in ["ruleset_id","ruleset_version","online_profile","match_format","mode","clock_profile","config_hash","bot_version","participant_kind","queue_pool","status"]}
dimensions.update({k:B for k in ["self_test","platform_interrupted","trustee_used"]})
define("StatisticDimensions",dimensions)
metric_names="completed_hands win_rate discard_loss_rate self_draw_rate average_net_points average_nonflower_points completed_matches average_rank average_raw_score average_standard_points average_decision_ms p95_decision_ms illegal_action_rate timeout_rate trustee_rate".split()
define("StatisticMetrics",{n:ref("Metric") for n in metric_names},[])
define("StatisticGroup",{"dimensions":ref("StatisticDimensions"),"comparable":B,"metrics":ref("StatisticMetrics")})
define("StatisticMatch",{"match_id":S,"created_at":T,"status":S,"rank":I,"raw_score":I,"standard_points":nullable(obj({"numerator":I,"denominator":I}))})
define("Statistics",{"groups":arr(ref("StatisticGroup")),"next_group":S,"statistics_version":const("settled-facts@1"),"comparable_policy":S,"profile":obj({"id":S,"display_name":S,"kind":enum("human","bot")}),"matches":arr(ref("StatisticMatch")),"next_before":S},["groups","next_group","statistics_version","comparable_policy"])
D["Statistics"]=dto("StatisticResponse")

# WebSocket message classes explicitly separate control receipts from view streams.
client=[dto("Action"),obj({"type":enum("hello","resume","ready","ping","heartbeat")}),obj({"type":const("resume_control"),"control_token":S}),obj({"type":const("authenticate"),"ticket":S})]
server=[{"allOf":[ref("ParticipantSnapshot"),{"required":["stream_id","view_seq"]}]},{"allOf":[ref("SpectatorSnapshot"),{"required":["stream_id","view_seq"]}]},ref("RoomSnapshot"),{"allOf":[ref("Decision"),{"required":["stream_id","view_seq","observation_ref"]}]},ref("CommandAck"),obj({"type":const("command_error"),"command_id":S,"error":obj({"code":S})}),obj({"type":const("error"),"error":obj({"code":S})}),obj({"type":const("heartbeat"),"server_time":T}),obj({"type":const("pong")}),obj({"type":const("seat_assigned"),**identity}),obj({"type":const("control_granted"),"control_token":S,"control_epoch":I}),obj({"type":const("control_readonly"),"control_epoch":I},["type"]),obj({"type":const("control_changed"),"control_epoch":I})]
D["WSClient"]={"oneOf":client};D["WSServer"]={"oneOf":server}
write(ROOT/"api/schemas/protocol.json",{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":BASE+"protocol.json","$defs":D})
for filename,name in [("ws-client.json","WSClient"),("ws-server.json","WSServer"),("spectator.json","PublicSnapshot")]:write(ROOT/"api/schemas"/filename,{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":BASE+filename,"$ref":"protocol.json#/$defs/"+name})

catalog={}
def endpoint(method,path,request,response,status=200,auth="cookie",note=""):
 catalog[(method,path)]=(request,response,status,auth,note)
for name,request,response,status in [("register","RegisterRequest","Accepted",202),("login","LoginRequest","LoginResponse",200),("verify-email","TokenRequest","Verified",200),("resend-verification","EmailRequest","Accepted",202),("forgot-password","EmailRequest","Accepted",202),("reset-password","ResetRequest","Reset",200),("logout",None,"LoggedOut",200)]:endpoint("POST","/v1/auth/"+name,request,response,status,"cookie" if name=="logout" else "origin","Mail links use fragments; GET never consumes a token. Generic unauthenticated responses prevent account enumeration.")
endpoint("GET","/v1/me",None,"LoginResponse")
endpoint("PATCH","/v1/me","NameRequest","UserEnvelope")
endpoint("GET","/v1/me/sessions",None,"SessionList")
endpoint("DELETE","/v1/me/sessions/{id}",None,"Revoked",note="Use id=others to preserve this session and revoke other devices.")
endpoint("GET","/v1/me/email-delivery",None,"EmailDelivery")
for path,request,response in [("password","PasswordChange","Changed"),("email-change/request","EmailChange","Accepted"),("email-change/confirm","TokenRequest","Changed"),("delete-account","PasswordRequest","Deleted")]:endpoint("POST","/v1/me/"+path,request,response,202 if response=="Accepted" else 200,note="Sensitive changes reverify credentials; password/email changes and deletion revoke existing human sessions.")
endpoint("GET","/v1/public/rooms",None,"Rooms",auth="public")
for path in ["/v1/public/rooms/{id}","/v1/rooms/{id}"]:endpoint("GET",path,None,"RoomEnvelope",auth="public")
endpoint("POST","/v1/rooms","CreateRoom","RoomCreated",201)
endpoint("POST","/v1/practice",None,"RoomEnvelope",201)
endpoint("POST","/v1/rooms/join","InviteCode","RoomEnvelope")
endpoint("POST","/v1/rooms/{id}/close",None,"Ok",note="Only the waiting-room owner may close the room and release its capacity reservation.")
for suffix,request,response in [("join","JoinRoom","RoomEnvelope"),("ready","Ready","RoomEnvelope"),("bots","AddBot","RoomEnvelope"),("start",None,"RoomEnvelope"),("leave",None,"LeftRoom"),("invite-rotate",None,"InviteCode"),("rematch",None,"RoomCreated"),("take-control",None,"PrivateSnapshot"),("actions","@Action","CommandAck")]:endpoint("POST",f"/v1/rooms/{{id}}/{suffix}",request,response,201 if suffix=="rematch" else 200,note="Verified active account and operation-specific seat/owner authority required. Actions also require X-Control-Token from take-control.")
endpoint("GET","/v1/rooms/{id}/view",None,"PrivateSnapshot")
for base,auth in [("/v1/queue","cookie"),("/v1/bot/queue","bot"),("/v1/bots/{id}/queue","cookie")]:
 endpoint("POST",base,"QueueRequest","QueueStatus",202,auth)
 endpoint("DELETE",base,None,"QueueStatus",200,auth)
endpoint("GET","/v1/queue",None,"HumanQueue")
for path,auth in [("/v1/me/active-match","cookie"),("/v1/bot/active-match","bot")]:endpoint("GET",path,None,"ActiveMatch",auth=auth)
for base in ["/v1/public/matches","/v1/me/matches"]:
 public="/public/" in base;endpoint("GET",base,None,"Matches",auth="public" if public else "cookie")
 endpoint("GET",base+"/{id}",None,"PublicMatchInfo" if public else "PrivateSnapshot",auth="public" if public else "cookie")
 endpoint("GET",base+"/{id}/replay",None,"PublicReplay" if public else "PrivateReplay",auth="public" if public else "cookie")
endpoint("GET","/v1/public/matches/{id}/snapshot",None,"PublicSnapshot",auth="public")
for public in [True,False]:endpoint("GET",f"/v1/{'public' if public else 'me'}/hands/{{id}}/replay",None,"PublicReplay" if public else "PrivateReplay",auth="public" if public else "cookie")
endpoint("GET","/v1/public/rooms/{id}/spectator",None,"PublicSnapshot",auth="public")
for path in ["/v1/public/rooms/{id}/spectator-ticket","/v1/public/matches/{id}/spectator-tickets"]:endpoint("POST",path,None,"SpectatorTicket",auth="public")
for path,response in [("/v1/bots","Bots"),("/v1/bots/{id}/versions","Versions"),("/v1/bots/{id}/credentials","Credentials")]:endpoint("GET",path,None,response)
endpoint("GET","/v1/bots/{id}/status",None,"@BotRuntimeStatus",note="Owner-only runtime presence, current room and queue state, and bounded recent error codes. Presence does not expose card observations or credentials.")
for path,request,response in [("/v1/bots","BotName","BotEnvelope"),("/v1/bots/{id}/versions","BotVersionCreate","VersionEnvelope"),("/v1/bots/{id}/credentials",None,"CredentialCreated")]:endpoint("POST",path,request,response,201)
endpoint("PATCH","/v1/bots/{id}","BotPatch","Ok")
endpoint("DELETE","/v1/bots/{id}/credentials/{credential}",None,"Ok")
endpoint("POST","/v1/bot-sessions","BotSessionRequest","BotSessionResponse",201,"key","Long key only here; exchange returns a 30-minute scoped runtime session. Never place tokens in URLs.")
for path in ["/v1/rulesets","/v1/public/rules"]:endpoint("GET",path,None,"RuleList",auth="public")
endpoint("GET","/v1/rulesets/{id}/versions/{version}",None,"RuleEnvelope",auth="public")
for path,auth in [("/v1/public/statistics","public"),("/v1/me/statistics","cookie"),("/v1/bots/{id}/statistics","cookie"),("/v1/public/profiles/{id}","public")]:endpoint("GET",path,None,"Statistics",auth=auth)
endpoint("GET","/v1/public/status",None,"PublicStatus",auth="public")
endpoint("GET","/v1/admin/status",None,"AdminStatus",auth="operator")
endpoint("GET","/v1/admin/audit",None,"Audit",auth="operator")
for path,request,response in [("maintenance","MaintenanceRequest","MaintenanceResponse"),("users/{id}/status","UserStatusRequest","Ok"),("bots/{id}/disable","Reason","BotSuspended"),("bots/{id}/restore","Reason","BotSuspended"),("rulesets/{id}/versions/{version}/status","RuleStatusRequest","RuleStatusResponse"),("matches/{id}/abort","Reason","AbortResponse")]:endpoint("POST","/v1/admin/"+path,request,response,auth="operator",note="Verified active operator role required; reason and outcome are audited. No scoring override or omniscient view exists.")
for path in ["/health/live","/health/ready"]:endpoint("GET",path,None,"Health",auth="public")
for kind in ["players","bots","spectators"]:endpoint("GET","/v1/ws/"+kind,None,None,101,"bot" if kind=="bots" else "cookie" if kind=="players" else "public","WebSocket upgrade. Schemas: /schemas/ws-client.json and /schemas/ws-server.json. Spectators must send authenticate(ticket) within 5 seconds before any data. Players may receive a read-only control lease; explicitly take-control for commands.")

actual=set()
for folder in [ROOT/"internal/auth",ROOT/"internal/platform"]:
 for source in folder.glob("*.go"):
  if not source.name.endswith("_test.go"):actual.update(re.findall(r'\.HandleFunc\("([A-Z]+) ([^" ]+)"',source.read_text()))
missing=actual-set(catalog)
if missing:raise SystemExit("Undocumented actual routes: "+str(sorted(missing)))
paths={}
def schema_ref(n):return {"$ref":"./schemas/dto.json#/$defs/"+n[1:]} if n.startswith("@") else {"$ref":"./schemas/protocol.json#/$defs/"+n}
for (method,path),(request,response,status,auth,note) in sorted(catalog.items()):
 if (method,path) not in actual and not path.startswith("/health/"):continue
 mutation=method not in ["GET","HEAD"]
 parameters=[{"name":name,"in":"path","required":True,"schema":S} for name in re.findall(r"{([^}]+)}",path)]
 security=[]
 if auth in ["cookie","operator"]:security=[{"CookieSession":[],**({"CSRFToken":[]} if mutation else {})}]
 if auth in ["key","bot"]:security=[{"BotKey" if auth=="key" else "BotSession":[]}]
 if mutation and auth in ["origin","cookie","operator"]:parameters.append({"name":"Origin","in":"header","required":True,"schema":S,"description":"Exactly the configured public origin."})
 if path.endswith("/actions"):parameters.append({"name":"X-Control-Token","in":"header","required":True,"schema":S,"description":"Current controller capability, separate from session and CSRF tokens."})
 if path.startswith("/v1/ws/"):parameters.append({"name":"room_id","in":"query","required":not path.endswith("/bots"),"schema":S})
 if "/replay" in path:
  for name in ["after","limit","hand_index"]:parameters.append({"name":name,"in":"query","schema":I,"description":"limit is capped at 200; after is the previous next_after."})
 if path in ["/v1/public/matches","/v1/me/matches"]:
  for name in ["mode","ruleset_id","ruleset_version","match_format","status","bot_id","before"]:parameters.append({"name":name,"in":"query","schema":S,"description":"before is the opaque next_before cursor from the previous page." if name=="before" else "Exact filter. On private history bot_id must belong to the authenticated owner."})
  parameters.append({"name":"limit","in":"query","schema":{"type":"integer","minimum":1,"maximum":100,"default":25}})
 if path.startswith("/v1/me/") and any(x in path for x in ["/matches/","/hands/"]):parameters.append({"name":"bot_id","in":"query","schema":S,"description":"Explicitly select an owned Bot perspective; omitted selects own human perspective."})
 if path.endswith("/statistics") or "/profiles/" in path:
  for name,typ in {**dimensions,"group_after":S,"before":S}.items():
   if name!="trustee_used":parameters.append({"name":name,"in":"query","schema":typ})
 if path=="/v1/admin/audit":parameters.append({"name":"before","in":"query","schema":I})
 operation={"operationId":re.sub(r'[^a-zA-Z0-9]+','_',method.lower()+"_"+path).strip('_'),"summary":method+" "+path,"description":note or "See schema and authorization requirements. Public routes always retain the discard-only audience.","security":security,"parameters":parameters,"responses":{str(status):{"description":"Success"},"default":{"description":"Rejected request. Authentication and Origin/CSRF checks precede domain authorization.","content":{"application/json":{"schema":schema_ref("Error")}}}}}
 if response:operation["responses"][str(status)]["content"]={"application/json":{"schema":schema_ref(response)}}
 if "/replay" in path or "/matches/{id}" in path or path.endswith("/view"):
  operation["responses"]["410"]={"description":"MATCH_ARCHIVED: after 30 days the card-bearing replay and snapshot data are erased. Public match details retain typed settled-score summaries only.","content":{"application/json":{"schema":schema_ref("Error")}}}
 if request:operation["requestBody"]={"required":True,"content":{"application/json":{"schema":schema_ref(request)}}}
 if auth=="operator":operation["x-required-role"]="operator"
 if path=="/health/ready":operation["responses"]["503"]={"description":"Database or migration readiness failed","content":{"text/plain":{"schema":S}}}
 paths.setdefault(path,{})[method.lower()]=operation
write(ROOT/"api/openapi.json",{"openapi":"3.1.0","jsonSchemaDialect":"https://json-schema.org/draft/2020-12/schema","info":{"title":"OpenMajiang public client API","version":"0.1.0","description":"Email accounts, human/Bot rooms and discard-only spectators. HTTP schemas describe the current Go wire contract; WebSocket schemas are separate. Errors never grant a hidden-card audience."},"servers":[{"url":"/"}],"paths":paths,"components":{"securitySchemes":{"CookieSession":{"type":"apiKey","in":"cookie","name":"omj_session"},"CSRFToken":{"type":"apiKey","in":"header","name":"X-CSRF-Token"},"BotKey":{"type":"http","scheme":"bearer","description":"Long Bot key, exchange-only."},"BotSession":{"type":"http","scheme":"bearer","description":"Short Bot runtime session, never an account cookie."}}}})
print(f"Documented {sum(len(v) for v in paths.values())} HTTP operations, {len(D)} protocol schemas")
