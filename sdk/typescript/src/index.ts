import { randomUUID } from 'node:crypto';
import WebSocket from 'ws';

export type Frame = Record<string, any>;
export interface Option { option_id: string; type: string; [key: string]: unknown }
export interface Decision extends Frame {
  match_id: string; hand_id: string; participant_id: string; seat_id: number;
  seat_assignment_version: number; control_epoch: number; decision_id: string;
  window_id: string; legal_actions: Option[]; server_time: string; deadline_at: string;
  observation_ref: {stream_id: string; view_seq: number};
}
export interface StrategyContext { observation: Readonly<Frame>; decision: Readonly<Decision>; signal: AbortSignal; deadline: number }
export type Strategy = (context: StrategyContext) => Promise<string> | string;
export class ControlTransferredError extends Error { constructor(){super('BOT_CONTROL_TRANSFERRED')} }
export interface EngineOptions { autoFlower?: boolean; deadlineMarginMs?: number; now?: () => number; onError?: (code: string) => void }

const identity = (f: Frame) => `${f.match_id}/${f.hand_id}/${f.participant_id}/${f.decision_id}`;
const freeze = <T>(v: T): T => { if(v && typeof v === 'object'){Object.freeze(v);for(const x of Object.values(v))freeze(x)} return v; };

/** Protocol state machine. Control ACKs never advance the observation sequence. */
export class DecisionEngine {
  private snapshot?: Frame;
  private stream = '';
  private seq = 0;
  private generation = 0;
  private task?: AbortController;
  private timer?: ReturnType<typeof setTimeout>;
  private current?: Decision;
  private deadline = 0;
  private pending = new Map<string, Frame>();
  private submitted = new Set<string>();
  private readonly now: () => number;
  constructor(private strategy: Strategy, private send: (frame: Frame) => void, private options: EngineOptions = {}) { this.now = options.now ?? Date.now; }

  receive(frame: Frame): void {
    if(frame.type === 'command_ack') {
      const old = this.pending.get(frame.command_id);
      if(old){this.submitted.add(identity(old));this.pending.delete(frame.command_id)}
      return;
    }
    if(frame.type === 'command_error') {
      const old = this.pending.get(frame.command_id);this.pending.delete(frame.command_id);
      if(old && frame.error?.code === 'STALE_CONTROL' && this.current && identity(old) === identity(this.current)) this.decide(this.current);
      this.options.onError?.(frame.error?.code ?? 'COMMAND_ERROR');return;
    }
    if(frame.type === 'heartbeat'){this.send({type:'heartbeat'});return}
    if(frame.type === 'control_changed'){this.disconnect();this.options.onError?.('STALE_CONTROL');return}
    if(frame.type === 'snapshot' || frame.type === 'room_snapshot') {
      this.cancel();this.current=undefined;
      if(!Number.isInteger(frame.view_seq)||frame.view_seq<1||typeof frame.stream_id!=='string'){this.resync();return}
      this.snapshot=freeze(structuredClone(frame));this.stream=frame.stream_id;this.seq=frame.view_seq;
      if(frame.recorded?.type==='command_ack')this.receive(frame.recorded);
      // Replaying the identical command is safe even after expiry; the server
      // checks a recorded receipt before checking its old lease or deadline.
      this.retryPending();
      if(frame.type==='room_snapshot' && frame.room?.status==='waiting')this.send({type:'ready'});
      if(this.submitted.size>4096)this.submitted.clear();
      return;
    }
    if(frame.type !== 'decision_request')return;
    const d=frame as Decision, snap=this.snapshot;
    if(!snap||frame.stream_id!==this.stream||frame.view_seq!==this.seq+1||d.observation_ref?.stream_id!==snap.stream_id||d.observation_ref?.view_seq!==snap.view_seq
      ||['match_id','hand_id','participant_id','seat_id','seat_assignment_version','control_epoch'].some(k=>d[k]!==snap[k])) {this.resync();return}
    this.seq=frame.view_seq;
    if(!Array.isArray(d.legal_actions)||d.legal_actions.length===0){this.resync();return}
    this.current=freeze(structuredClone(d));
    const remaining=Date.parse(d.deadline_at)-Date.parse(d.server_time)-(this.options.deadlineMarginMs??50);
    this.deadline=this.now()+remaining;
    if(!Number.isFinite(remaining)||remaining<=0)return;
    if(this.submitted.has(identity(d)))return;
    const prior=[...this.pending.values()].find(p=>identity(p)===identity(d));
    if(prior){this.send(prior);return}
    this.decide(this.current);
  }

  private decide(d: Decision): void {
    this.cancel();if(!this.snapshot||this.now()>=this.deadline||this.submitted.has(identity(d)))return;
    const generation=this.generation, controller=new AbortController();this.task=controller;
    const finish=(optionID: string)=>{
      if(controller.signal.aborted||generation!==this.generation||this.now()>=this.deadline)return;
      const option=d.legal_actions.find(o=>o.option_id===optionID);if(!option){this.options.onError?.('INVALID_STRATEGY_OPTION');return}
      this.cancel();
      const action:Frame={type:'submit_action',protocol_version:'1.0',command_id:randomUUID(),option_id:optionID};
      for(const key of ['match_id','hand_id','participant_id','seat_id','seat_assignment_version','control_epoch','decision_id','window_id'])action[key]=d[key];
      this.pending.set(action.command_id,action);this.send(action);
    };
    this.timer=setTimeout(()=>controller.abort(),Math.max(0,this.deadline-this.now()));
    const onlyPass=d.legal_actions.length===1&&d.legal_actions[0].type==='pass';
    const flower=this.options.autoFlower!==false&&d.legal_actions.find(o=>o.type==='replace_flower');
    if(onlyPass){finish(d.legal_actions[0].option_id);return}
    if(flower){finish(flower.option_id);return}
    const observation=this.snapshot;
    Promise.resolve().then(()=>{if(controller.signal.aborted)throw new Error('CANCELLED');return this.strategy({observation,decision:d,signal:controller.signal,deadline:this.deadline})})
      .then(finish).catch(()=>{if(!controller.signal.aborted)this.options.onError?.('STRATEGY_FAILED')});
  }
  private cancel():void {this.generation++;this.task?.abort();this.task=undefined;if(this.timer)clearTimeout(this.timer);this.timer=undefined}
  private resync():void {this.cancel();this.snapshot=undefined;this.current=undefined;this.send({type:'resume'})}
  retryPending():void {if(!this.snapshot)return;for(const p of this.pending.values()){if(p.match_id===this.snapshot.match_id&&p.hand_id===this.snapshot.hand_id)this.send(p);else this.pending.delete(p.command_id)}}
  disconnect():void {this.cancel();this.snapshot=undefined;this.current=undefined;this.stream='';this.seq=0}
  get pendingCount():number{return this.pending.size}
}

export interface BotOptions extends EngineOptions {
  baseURL: string; apiKey: string; rulesets?: {id:string;version:string}[];
  queue?: {ruleset_id:string;ruleset_version:string;match_format:string;continuous:boolean};
  onState?: (state: string) => void;
  onFrame?: (frame: Readonly<Frame>) => void;
}

/** Self-hosted runtime: secrets stay in this process, never in a URL or log. */
export class BotClient {
  private session='';private expires=0;private ws?:WebSocket;
  private engine:DecisionEngine;
  constructor(private options:BotOptions,strategy:Strategy){
    const u=new URL(options.baseURL);if(!['http:','https:'].includes(u.protocol)||u.username||u.password)throw new Error('invalid base URL');
    if(u.protocol==='http:'&&!['localhost','127.0.0.1','[::1]'].includes(u.hostname))throw new Error('remote Bot connections require HTTPS');
    this.engine=new DecisionEngine(strategy,f=>{if(this.ws?.readyState===WebSocket.OPEN)this.ws.send(JSON.stringify(f))},options);
  }
  private async request(path:string,method='GET',body?:unknown,signal?:AbortSignal,long=false):Promise<any>{
    const response=await fetch(new URL(path,this.options.baseURL),{method,signal,redirect:'error',headers:{Authorization:`Bearer ${long?this.options.apiKey:this.session}`,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
    if(response.status===401){this.session='';throw new Error('AUTH_EXPIRED')}
    if(!response.ok)throw new Error(`HTTP_${response.status}`);return response.json();
  }
  private async authenticate(signal:AbortSignal):Promise<void>{if(this.session&&Date.now()<this.expires-60000)return;const v=await this.request('/v1/bot-sessions','POST',{protocol_version:'1.0',rulesets:this.options.rulesets??[{id:'openmajiang.mcr',version:'1.0.0'}]},signal,true);this.session=v.session_token;this.expires=Date.parse(v.expires_at)}
  reconnect():void {this.ws?.close()}
  async leaveQueue(signal?:AbortSignal):Promise<void>{await this.request('/v1/bot/queue','DELETE',{},signal)}
  async run(signal:AbortSignal):Promise<void>{
    let attempt=0,queued=false;
    while(!signal.aborted){try{
      await this.authenticate(signal);
      const active=await this.request('/v1/bot/active-match','GET',undefined,signal);
      if(!active.room){
        if(this.options.queue&&!queued){await this.request('/v1/bot/queue','POST',this.options.queue,signal);queued=true}
        await sleep(1000,signal);continue;
      }
      queued=false;this.options.onState?.('connected');const completed=await this.connect(active.room.id,signal);attempt=0;
      if(completed&&this.options.queue&&!this.options.queue.continuous)return;
    }catch(error){if(signal.aborted)break;if(error instanceof ControlTransferredError){this.options.onState?.('control_transferred');throw error}this.options.onState?.('reconnecting');attempt++;await sleep(Math.min(30000,500*2**Math.min(attempt,6))*(0.75+Math.random()/2),signal)} }
    this.engine.disconnect();this.ws?.close();
  }
  private connect(roomID:string,signal:AbortSignal):Promise<boolean>{return new Promise((resolve,reject)=>{
    const url=new URL('/v1/ws/bots',this.options.baseURL);url.protocol=url.protocol==='https:'?'wss:':'ws:';url.searchParams.set('room_id',roomID);
    const ws=new WebSocket(url,{headers:{Authorization:`Bearer ${this.session}`},maxPayload:1024*1024,handshakeTimeout:10000,followRedirects:false});this.ws=ws;
    let completed=false,plannedRefresh=false,transferred=false;
    const abort=()=>ws.close();signal.addEventListener('abort',abort,{once:true});
    const retry=setInterval(()=>this.engine.retryPending(),500);
    const refresh=setTimeout(()=>{plannedRefresh=true;ws.close()},Math.max(0,this.expires-Date.now()-30000));
    ws.on('message',raw=>{try{const frame=JSON.parse(raw.toString());this.options.onFrame?.(freeze(structuredClone(frame)));this.engine.receive(frame);if(frame.type==='control_changed'){transferred=true;ws.close()}if(frame.type==='snapshot'&&frame.status&&frame.status!=='active'){completed=true;ws.close()}}catch{ws.close(1002,'invalid frame')}});
    ws.on('error',()=>{});
    ws.on('close',()=>{clearInterval(retry);clearTimeout(refresh);signal.removeEventListener('abort',abort);this.engine.disconnect();if(transferred)reject(new ControlTransferredError());else if(completed||signal.aborted||plannedRefresh)resolve(completed);else reject(new Error('CONNECTION_LOST'))});
  })}
}

function sleep(ms:number,signal:AbortSignal):Promise<void>{return new Promise(resolve=>{if(signal.aborted){resolve();return}const abort=()=>{clearTimeout(timer);resolve()};const timer=setTimeout(()=>{signal.removeEventListener('abort',abort);resolve()},ms);signal.addEventListener('abort',abort,{once:true})})}

/** Connectivity baseline only; it is not a strong Mahjong policy. */
export const firstLegal:Strategy=({decision})=>(decision.legal_actions.find(o=>o.type==='hu')??decision.legal_actions.find(o=>o.type==='discard')??decision.legal_actions[0]).option_id;
