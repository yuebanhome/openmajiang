import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
const mailpitURL = process.env.MAILPIT_HTTP_URL || 'http://127.0.0.1:8025';
const baseURL = process.env.PUBLIC_BASE_URL || 'http://127.0.0.1:8080';
const password = 'OpenMajiang-e2e-password-2026!';
async function mailLink(request:APIRequestContext,email:string,path:string) {
 let link='';
 await expect.poll(async()=>{
  const response=await request.get(`${mailpitURL}/api/v1/messages`);if(!response.ok())return false;
  const listing=await response.json();
  for(const item of listing.messages??[]){if(!(item.To??[]).some((to:{Address?:string})=>to.Address===email))continue;const message=await (await request.get(`${mailpitURL}/api/v1/message/${item.ID}`)).json();const text=String(message.Text??'')+' '+String(message.HTML??'');const found=text.match(new RegExp(`https?:[^\\s"<>]+${path}#token=[A-Za-z0-9_=-]+`));if(found){link=found[0];return true;}}
  return false;
 },{timeout:40_000,message:`SMTP mail containing ${path} for this newly registered user`}).toBe(true);
 return link;
}
async function login(page:Page,email:string,pw=password){await page.goto('/auth/login');await page.getByLabel('邮箱',{exact:true}).fill(email);await page.getByLabel('密码',{exact:true}).fill(pw);await page.getByRole('button',{name:'登录',exact:true}).click();await expect(page).toHaveURL(`${baseURL}/`);}
function rejectHiddenKeys(value:unknown) {if(!value||typeof value!=='object')return;for(const [key,child]of Object.entries(value)){expect(['hand','tile_id','wall','melds','flowers','drawn_tile_id','legal_actions','fan_items','decomposition','winning_tiles']).not.toContain(key);rejectHiddenKeys(child);}}

test('real account, mail, playable table, anonymous discard view, replay and credential lifecycle',async({page,browser,request})=>{
 const email=`browser-${Date.now()}@example.test`;const name='浏览器验收牌手';
 await page.goto('/');await expect(page.getByRole('heading',{name:/一起打麻将/})).toBeVisible();
 await page.getByRole('link',{name:'加入牌桌 ↗'}).click();
 await page.getByLabel('邮箱',{exact:true}).fill(email);await page.getByLabel('展示名',{exact:true}).fill(name);await page.getByLabel('密码',{exact:true}).fill(password);await page.getByRole('checkbox').check();await page.getByRole('button',{name:'注册并发送验证邮件'}).click();await expect(page.getByRole('status')).toContainText('验证邮件');
 const verifyURL=await mailLink(request,email,'/verify-email');await page.goto(verifyURL);await page.getByRole('button',{name:'验证邮箱',exact:true}).click();await expect(page.getByRole('status')).toContainText('邮箱已验证');
 await login(page,email);await expect(page.getByRole('link',{name})).toBeVisible();
 await page.getByRole('link',{name}).click();await expect(page.getByText('当前设备',{exact:true})).toBeVisible();
 await page.goto('/');await page.getByRole('button',{name:'与 Bot 练习',exact:true}).click();await expect(page).toHaveURL(/\/play\/room_/);const roomID=new URL(page.url()).pathname.split('/').pop()!;
 await expect(page.locator('.hand-tiles .tile')).not.toHaveCount(0);await expect(page.getByText('实时连接',{exact:true})).toBeVisible();
 // The first human seat is the first dealer. Submit one real legal discard through the UI.
 const playable=page.locator('.hand-tiles button.tile:not([disabled])');await expect(playable.first()).toBeEnabled();await playable.first().click();const actionResponse=page.waitForResponse(r=>r.url().endsWith(`/v1/rooms/${roomID}/actions`)&&r.request().method()==='POST');await page.getByRole('button',{name:/^打出 /}).click();expect((await actionResponse).status()).toBe(200);
 const privateState=await page.evaluate(async(id)=>await(await fetch(`/v1/rooms/${id}/view`)).json(),roomID);expect(privateState.match_id).toBeTruthy();const matchID=privateState.match_id;
 const guest=await browser.newContext();const watcher=await guest.newPage();await watcher.goto(`${baseURL}/watch/${roomID}`);await expect(watcher.getByText('所有人都可以观战，只展示已经打出的牌。')).toBeVisible();await expect(watcher.locator('[data-testid="spectator-table"] .tile').first()).toBeVisible();expect(await watcher.locator('.hand-tiles,.meld-row,.flowers-row').count()).toBe(0);
 const publicState=await watcher.evaluate(async(id)=>await(await fetch(`/v1/public/rooms/${id}/spectator`)).json(),roomID);rejectHiddenKeys(publicState.view);expect(publicState.view.discards.length).toBeGreaterThan(0);expect(await watcher.evaluate(async(id)=>(await fetch(`/v1/rooms/${id}/view`)).status,roomID)).toBe(401);
 // A refresh must re-authenticate control and render the actual current private hand.
 await page.reload();await expect(page.getByText('实时连接',{exact:true})).toBeVisible();await expect(page.locator('.hand-tiles .tile').first()).toBeVisible();
 await watcher.goto(`${baseURL}/matches/${matchID}`);await expect(watcher.getByRole('heading',{name:'公开弃牌牌谱'})).toBeVisible();await expect(watcher.getByTestId('spectator-table')).toBeVisible();expect(await watcher.locator('.hand-tiles,.meld-row,.flowers-row').count()).toBe(0);
 await page.goto(`/replays/${matchID}`);await expect(page.getByRole('heading',{name:'本人视角复盘'})).toBeVisible();await expect(page.locator('.hand-tiles .tile').first()).toBeVisible();
 // Developer controls hit the authenticated backend, not a local demo state.
 await page.goto('/bots');await page.getByRole('button',{name:'＋ 创建 Bot',exact:true}).click();await page.getByLabel('Bot 名称').fill('BrowserBot');await page.getByRole('button',{name:'创建 Bot',exact:true}).click();await expect(page.getByRole('heading',{name:'BrowserBot',exact:true}).last()).toBeVisible();await page.getByPlaceholder('版本号，例如 0.1.0').fill('e2e-1');await page.getByRole('button',{name:'发布版本',exact:true}).click();await expect(page.getByText('e2e-1',{exact:true})).toBeVisible();await page.getByRole('button',{name:'生成新 Key',exact:true}).click();await expect(page.locator('.secret')).toContainText('omj_bot_');await page.getByRole('button',{name:'已保存，关闭'}).click();expect(await page.locator('.secret').count()).toBe(0);
 page.once('dialog',d=>d.accept());await page.getByRole('button',{name:'撤销',exact:true}).first().click();await expect(page.getByRole('button',{name:'撤销',exact:true})).toHaveCount(0);
 // Mobile viewport stays inside the screen; public visibility does not change.
 await watcher.setViewportSize({width:390,height:844});await watcher.goto(`${baseURL}/watch/${roomID}`);await expect(watcher.getByTestId('spectator-table')).toBeVisible();expect(await watcher.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);expect(await watcher.locator('.hand-tiles,.meld-row,.flowers-row').count()).toBe(0);await guest.close();
 await page.getByRole('button',{name:'退出',exact:true}).click();await page.goto('/auth/forgot');await page.getByLabel('邮箱',{exact:true}).fill(email);await page.getByRole('button',{name:'发送重置邮件'}).click();const resetURL=await mailLink(request,email,'/reset-password');await page.goto(resetURL);const newPassword=password+'-new';await page.getByLabel('密码',{exact:true}).fill(newPassword);await page.getByRole('button',{name:'更新密码'}).click();await expect(page.getByRole('status')).toContainText('密码已重置');await login(page,email,newPassword);await expect(page.getByRole('link',{name})).toBeVisible();
});
