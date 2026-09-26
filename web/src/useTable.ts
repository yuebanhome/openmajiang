import { useCallback, useEffect, useRef, useState } from 'react';
import { api, APIError, errorMessage, post } from './api';
import { normalizeSpectator } from './spectator';
import { normalizeParticipant } from './table-state';
import type { ParticipantView, SpectatorView } from './types';
export function useTable(id:string, spectator:boolean) {
 const [view,setView]=useState<SpectatorView|ParticipantView>();const [error,setError]=useState('');const [connected,setConnected]=useState(false);const [nonce,setNonce]=useState(0);const [controlStatus,setControlStatus]=useState<'connecting'|'readonly'|'active'>('connecting');const token=useRef('');const resumePending=useRef(false);const socketRef=useRef<WebSocket|undefined>(undefined);
 const refresh=useCallback(()=>setNonce(n=>n+1),[]);
 const takeControl=useCallback(async()=>{const result=await post<{control_token:string}>(`/v1/rooms/${encodeURIComponent(id)}/take-control`);token.current=result.control_token;resumePending.current=true;setControlStatus('connecting');if(socketRef.current?.readyState===WebSocket.OPEN)socketRef.current.send(JSON.stringify({type:'resume_control',control_token:token.current}));else setNonce(n=>n+1);},[id]);
 const actionHeaders=()=>({'X-Control-Token':token.current});
 useEffect(()=>{let active=true;let socket:WebSocket|undefined;let abort:AbortController|undefined;let retry:ReturnType<typeof setTimeout>|undefined;let retries=0;let loading=false;
 setView(undefined);setError('');setConnected(false);setControlStatus('connecting');
 const decode=(data:unknown)=>spectator?normalizeSpectator(data):normalizeParticipant(data);
 const load=async()=>{if(loading)return;loading=true;abort=new AbortController();try{const data=await api<unknown>(spectator?`/v1/public/rooms/${encodeURIComponent(id)}/spectator`:`/v1/rooms/${encodeURIComponent(id)}/view`,{signal:abort.signal});if(active){setView(decode(data));setError('');}}catch(e){if(active&&!(e instanceof DOMException&&e.name==='AbortError')){setError(errorMessage(e));if(e instanceof APIError&&(e.status===401||e.status===403)){token.current='';setControlStatus('readonly');setView(undefined);}}}finally{loading=false;}};
 const connect=async()=>{try{const ticket=spectator?await post<{ticket:string}>(`/v1/public/rooms/${encodeURIComponent(id)}/spectator-ticket`):undefined;if(!active)return;socket=new WebSocket(`${location.protocol==='https:'?'wss:':'ws:'}//${location.host}/v1/ws/${spectator?'spectators':'players'}?room_id=${encodeURIComponent(id)}`);socketRef.current=socket;resumePending.current=false;socket.onopen=()=>{if(ticket)socket?.send(JSON.stringify({type:'authenticate',ticket:ticket.ticket}));if(active){setConnected(true);retries=0;void load();}};
 socket.onmessage=event=>{if(!active)return;let message:Record<string,unknown>;try{message=JSON.parse(String(event.data));}catch{return;}switch(message.type){case'control_granted':if(typeof message.control_token==='string'){token.current=message.control_token;resumePending.current=false;setControlStatus('active');void load();}break;case'control_readonly':if(token.current&&!resumePending.current){resumePending.current=true;socket?.send(JSON.stringify({type:'resume_control',control_token:token.current}));}else{token.current='';resumePending.current=false;setControlStatus('readonly');}break;case'control_changed':token.current='';setControlStatus('readonly');break;case'heartbeat':socket?.send(JSON.stringify({type:'heartbeat'}));break;case'snapshot':case'spectator_snapshot':case'room_snapshot':case'decision_request':case'seat_assigned':case'action_ack':void load();break;}};
 socket.onerror=()=>socket?.close();socket.onclose=()=>{if(active){setConnected(false);setControlStatus(c=>c==='readonly'?c:'connecting');retry=setTimeout(()=>void connect(),Math.min(1000*2**retries++,15000));}};}catch{if(active)retry=setTimeout(()=>void connect(),Math.min(1000*2**retries++,15000));}};
 void load();void connect();const interval=setInterval(()=>{if(document.visibilityState==='visible')void load();},2000); const onFocus=()=>void load();window.addEventListener('focus',onFocus);
 return()=>{active=false;abort?.abort();socket?.close();if(retry)clearTimeout(retry);clearInterval(interval);window.removeEventListener('focus',onFocus);};
 },[id,spectator,nonce]);
 return {view,error,connected,refresh,controlStatus,takeControl,actionHeaders};
}
