import { useCallback, useEffect, useState } from 'react';
import { api, errorMessage, post } from './api';
import { normalizeSpectator } from './spectator';
import { normalizeParticipant } from './table-state';
import type { ParticipantView, SpectatorView } from './types';
export function useTable(id:string, spectator:boolean) {
 const [view,setView]=useState<SpectatorView|ParticipantView>();const [error,setError]=useState('');const [connected,setConnected]=useState(false);const [nonce,setNonce]=useState(0);
 const refresh=useCallback(()=>setNonce(n=>n+1),[]);
 useEffect(()=>{let active=true;let socket:WebSocket|undefined;let abort:AbortController|undefined;let retry:ReturnType<typeof setTimeout>|undefined;let retries=0;let loading=false;
 setView(undefined);setError('');setConnected(false);
 const decode=(data:unknown)=>spectator?normalizeSpectator(data):normalizeParticipant(data);
 const load=async()=>{if(loading)return;loading=true;abort=new AbortController();try{const data=await api<unknown>(spectator?`/v1/public/rooms/${encodeURIComponent(id)}/spectator`:`/v1/rooms/${encodeURIComponent(id)}/view`,{signal:abort.signal});if(active){setView(decode(data));setError('');}}catch(e){if(active&&!(e instanceof DOMException&&e.name==='AbortError'))setError(errorMessage(e));}finally{loading=false;}};
 const connect=async()=>{try{const ticket=spectator?await post<{ticket:string}>(`/v1/public/rooms/${encodeURIComponent(id)}/spectator-ticket`):undefined;if(!active)return;socket=new WebSocket(`${location.protocol==='https:'?'wss:':'ws:'}//${location.host}/v1/ws/${spectator?'spectators':'players'}?room_id=${encodeURIComponent(id)}`);socket.onopen=()=>{if(ticket)socket?.send(JSON.stringify({type:'authenticate',ticket:ticket.ticket}));if(active){setConnected(true);retries=0;void load();}};socket.onmessage=()=>{void load();};socket.onerror=()=>socket?.close();socket.onclose=()=>{if(active){setConnected(false);retry=setTimeout(()=>void connect(),Math.min(1000*2**retries++,15000));}};}catch{if(active)retry=setTimeout(()=>void connect(),Math.min(1000*2**retries++,15000));}};
 void load();void connect();const interval=setInterval(()=>{if(document.visibilityState==='visible')void load();},2000); const onFocus=()=>void load();window.addEventListener('focus',onFocus);
 return()=>{active=false;abort?.abort();socket?.close();if(retry)clearTimeout(retry);clearInterval(interval);window.removeEventListener('focus',onFocus);};
 },[id,spectator,nonce]);
 return {view,error,connected,refresh};
}
