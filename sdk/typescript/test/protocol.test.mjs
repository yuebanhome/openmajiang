import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {BotClient,ControlTransferredError,DecisionEngine,firstLegal} from '../dist/index.js';
const fixture=JSON.parse(await readFile(new URL('../../../api/fixtures/decision.json',import.meta.url),'utf8'));
const pair=()=>structuredClone(fixture);
const tick=()=>new Promise(resolve=>setImmediate(resolve));

test('snapshot barrier and ACK controls cannot become observations',async()=>{
 const sent=[];let calls=0;const engine=new DecisionEngine(()=>{calls++;return 'discard_1'},f=>sent.push(f));
 const {snapshot,decision}=pair();engine.receive(decision);await tick();assert.equal(calls,0);assert.equal(sent[0].type,'resume');
 engine.receive(snapshot);engine.receive({type:'command_ack',command_id:'unrelated',view_seq:100});engine.receive(decision);await tick();
 assert.equal(calls,1);assert.equal(sent.at(-1).option_id,'discard_1');assert.equal(sent.at(-1).control_epoch,7);engine.disconnect();
});
test('retries preserve the exact command and ACK means recorded only',async()=>{
 const sent=[];const engine=new DecisionEngine(firstLegal,f=>sent.push(f));const{snapshot,decision}=pair();engine.receive(snapshot);engine.receive(decision);await tick();const command=sent.at(-1);engine.retryPending();assert.deepEqual(sent.at(-1),command);assert.equal(engine.pendingCount,1);
 engine.receive({type:'command_ack',command_id:command.command_id,status:'recorded'});assert.equal(engine.pendingCount,0);engine.receive(snapshot);engine.receive(decision);await tick();assert.equal(sent.filter(x=>x.type==='submit_action').length,2);engine.disconnect();
});
test('stale observation, hand and epoch never invoke strategy',async()=>{
 for(const field of ['control_epoch','hand_id','participant_id','seat_assignment_version']){let calls=0;const sent=[];const engine=new DecisionEngine(()=>{calls++;return 'discard_1'},f=>sent.push(f));const{snapshot,decision}=pair();decision[field]='wrong';engine.receive(snapshot);engine.receive(decision);await tick();assert.equal(calls,0);assert.equal(sent.at(-1).type,'resume');engine.disconnect()}
});
test('only-pass and optional automatic flowers do not invoke strategy',async()=>{
 for(const type of ['pass','replace_flower']){let calls=0;const sent=[];const engine=new DecisionEngine(()=>{calls++;return 'x'},f=>sent.push(f));const{snapshot,decision}=pair();decision.legal_actions=[{type,option_id:'x'}];engine.receive(snapshot);engine.receive(decision);await tick();assert.equal(calls,0);assert.equal(sent.at(-1).option_id,'x');engine.disconnect()}
 let calls=0;const engine=new DecisionEngine(()=>{calls++;return 'flower'},()=>{},{autoFlower:false});const{snapshot,decision}=pair();decision.legal_actions=[{type:'replace_flower',option_id:'flower'}];engine.receive(snapshot);engine.receive(decision);await tick();assert.equal(calls,1);engine.disconnect();
});
test('new snapshots cancel strategy and discard late results',async()=>{
 let finish;let signal;const sent=[];const engine=new DecisionEngine(c=>{signal=c.signal;return new Promise(r=>finish=r)},f=>sent.push(f));const{snapshot,decision}=pair();engine.receive(snapshot);engine.receive(decision);await tick();engine.receive({...snapshot,view_seq:3});assert.equal(signal.aborted,true);finish('discard_1');await tick();assert.equal(sent.filter(x=>x.type==='submit_action').length,0);engine.disconnect();
});
test('expired decisions and invalid strategy actions cannot be sent',async()=>{
 const sent=[];const engine=new DecisionEngine(()=> 'not-an-option',f=>sent.push(f));const{snapshot,decision}=pair();engine.receive(snapshot);engine.receive(decision);await tick();assert.equal(sent.length,0);engine.disconnect();decision.deadline_at=decision.server_time;engine.receive(snapshot);engine.receive(decision);await tick();assert.equal(sent.length,0);engine.disconnect();
});

test('continuous runtime discovers new rooms after terminal matches',async()=>{
 const stop=new AbortController(),rooms=[];let round=0;
 const client=new BotClient({baseURL:'http://localhost:8080',apiKey:'test-key',queue:{ruleset_id:'openmajiang.mcr',ruleset_version:'1.0.0',match_format:'practice_1',continuous:true}},firstLegal);
 client.authenticate=async()=>{};
 client.request=async()=>({room:{id:`room_${++round}`}});
 client.connect=async(room)=>{rooms.push(room);if(rooms.length===2)stop.abort();return true};
 await client.run(stop.signal);assert.deepEqual(rooms,['room_1','room_2']);
});

test('planned renewal does not end a once-only match and takeover is terminal',async()=>{
 const client=new BotClient({baseURL:'http://localhost:8080',apiKey:'test-key',queue:{ruleset_id:'openmajiang.mcr',ruleset_version:'1.0.0',match_format:'practice_1',continuous:false}},firstLegal);
 client.authenticate=async()=>{};client.request=async()=>({room:{id:'same_room'}});let connects=0;
 client.connect=async()=>++connects===2;
 await client.run(new AbortController().signal);assert.equal(connects,2);
 client.connect=async()=>{throw new ControlTransferredError()};
 await assert.rejects(client.run(new AbortController().signal),ControlTransferredError);
});
