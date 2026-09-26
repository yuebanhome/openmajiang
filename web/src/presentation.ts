export function terminalState(status:string|undefined,phase:string|undefined){
 const reasons:Record<string,string>={completed:'对局已完成',ended:'对局已结束',ended_early:'玩家退出或未恢复连接，剩余赛程提前结束',early_terminated:'剩余赛程提前结束',aborted_by_server:'平台中断：服务未能在恢复期限内继续',missing_ruleset:'平台中断：当前规则版本不可用',invalid_state:'平台中断：对局状态未通过校验',aborted:'平台中断',closed:'房间已关闭'};
 if(status&&reasons[status])return{ended:true,reason:reasons[status],interrupted:!['completed','ended','closed'].includes(status)};
 if(phase==='ended')return{ended:true,reason:'对局已结束',interrupted:false};
 return{ended:false,reason:'',interrupted:false};
}
