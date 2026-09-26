// A real, independent TypeScript SDK runtime for the WSS gate. Configuration
// arrives over stdin, keeping the API key out of command arguments and logs.
import readline from 'node:readline';
import {BotClient, firstLegal} from '../typescript/dist/index.js';

const input=readline.createInterface({input:process.stdin});
const stop=new AbortController();
let configure;
const configuration=new Promise(resolve=>configure=resolve);
input.on('line',line=>{const value=JSON.parse(line);if(value.type==='stop')stop.abort();else configure(value)});
input.on('close',()=>stop.abort());
const options=await configuration;
const {index,base_url,api_key,match_format}=options;
const emit=event=>process.stdout.write(JSON.stringify({bot:index,...event})+'\n');
const decisions=new Set(),acks=new Set(),errors={},states={};
let lastEmit=0;
const client=new BotClient({baseURL:base_url,apiKey:api_key,
 queue:{ruleset_id:'openmajiang.mcr',ruleset_version:'1.0.0',match_format,continuous:true},
 onError:code=>errors[code]=(errors[code]??0)+1,
 onState:state=>{states[state]=(states[state]??0)+1;emit({type:'connection',state,count:states[state]})},
 onFrame:frame=>{
  if(frame.type==='decision_request')decisions.add(frame.match_id+'/'+frame.decision_id);
  if(frame.type==='command_ack')acks.add(frame.command_id);
  if(frame.type!=='snapshot')return;
  if(frame.recorded?.type==='command_ack')acks.add(frame.recorded.command_id);
  const view=frame.view??{},result=view.result;
  if(result||Date.now()-lastEmit>=10000){lastEmit=Date.now();emit({type:'snapshot',match_id:frame.match_id,
   room_id:frame.room?.id,hand_index:frame.hand_index,status:frame.status,interrupted:frame.platform_interrupted??false,
   phase:view.phase,result:result?{method:result.method,winner_seat:result.winner_seat,score_deltas:result.score_deltas}:null,
   decisions:decisions.size,acks:acks.size,errors:{...errors},self_timeout_count:frame.self_timeout_count??null,
   reaction_timeout_count:frame.reaction_timeout_count??null});}
 }},firstLegal);
try{await client.run(stop.signal)}
catch(error){emit({type:'worker_error',error:error?.constructor?.name??'Error'});process.exitCode=1}
finally{
 try{await client.leaveQueue()}catch{}
 emit({type:'stopped',decisions:decisions.size,acks:acks.size,errors,states});input.close();process.stdin.pause();
}
