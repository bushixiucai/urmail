async (page) => {
 const fixture=__URMAIL_BROWSER_FIXTURE__;
 const base=fixture.base_url, account=fixture.accounts[0];
 const evidence=__URMAIL_BROWSER_EVIDENCE__;
 const requests=[];const errors=[];
 page.on('response',response=>{if(response.url().startsWith(base+'/api/'))requests.push({path:new URL(response.url()).pathname,status:response.status(),method:response.request().method()})});
 page.on('pageerror',error=>errors.push(error.message));
 const login=async credentials=>{await page.evaluate(()=>sessionStorage.clear());await page.goto(base+'/login');await page.locator('#email').fill(credentials.email);await page.locator('#password').fill(credentials.password);await page.locator('form button[type="submit"]').click();await page.waitForURL(base+'/')};
 const portalGet=async path=>page.evaluate(async path=>{const response=await fetch(path,{headers:{Authorization:'Bearer '+sessionStorage.getItem('urmail.session-token')}});if(!response.ok)throw Error('Portal read '+path+' status'+response.status);return response.json()},path);
 await login(account);

 // Exercise the real locale switch and authenticated mobile routes before
 // returning to Chinese for the existing state-changing closure chains.
 await page.getByRole('button',{name:/^(English|中文)$/}).waitFor();
 const englishButton=page.getByRole('button',{name:'English',exact:true});
 if(!(await englishButton.isVisible())){
  const initialChineseCatalogue=page.waitForResponse(r=>r.url()===base+'/api/v1/i18n/zh-CN'&&r.status()===200);
  await page.getByRole('button',{name:'中文',exact:true}).click();await initialChineseCatalogue;await englishButton.waitFor();
 }
 const englishCatalogueResponse=page.waitForResponse(r=>r.url()===base+'/api/v1/i18n/en'&&r.status()===200);
 await englishButton.click();
 const englishCatalogue=await(await englishCatalogueResponse).json();
 if(englishCatalogue['page.billing.title']!=='Billing and plan'||englishCatalogue['layout.open_menu']!=='Open menu')throw Error('English catalogue not actually loaded from the backend');
 await page.getByRole('button',{name:'中文',exact:true}).waitFor();
 await page.setViewportSize({width:375,height:812});
 const mobileEnglishRoutes=[['/domains','page.domains.title'],['/mailboxes','page.mailboxes.title'],['/aliases','page.aliases.title'],['/billing','page.billing.title'],['/marketing','nav.marketing'],['/support','nav.support'],['/notifications','notify.title'],['/settings/mfa','page.settings.mfa.title'],['/ai','page.ai.title'],['/archive','archive.title'],['/delivery','delivery.title'],['/audit','page.audit.title']];
 const englishMobileChecks=[],englishMobileAria=[];
 const englishScreenshotRoutes=new Set(['/billing','/archive','/delivery','/support','/ai','/audit']);
 for(const [route,titleKey] of mobileEnglishRoutes){
  const requestStart=requests.length,errorStart=errors.length;
  await page.goto(base+route);await page.waitForLoadState('networkidle');
  if(new URL(page.url()).pathname!==route)throw Error('English mobile authenticated route redirected '+route+' to '+new URL(page.url()).pathname);
  await page.locator('main').getByText(englishCatalogue[titleKey],{exact:true}).first().waitFor();
  await page.getByRole('button',{name:'Open menu',exact:true}).waitFor();
  const visibleText=await page.locator('body').innerText();
  const rawTranslationKeys=[...new Set(visibleText.match(/\b(?:page|layout|nav|error|state|billing|archive|delivery|support|marketing|notify|ai|status|action|auth|common|form|label|unit|validation|mfa|branding|settings|compliance|table)\.[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*/g)||[])];
  if(rawTranslationKeys.length)throw Error('Raw translation keys on English mobile '+route+': '+rawTranslationKeys.join(','));
  const dimensions=await page.evaluate(()=>({viewport:innerWidth,page:document.documentElement.scrollWidth}));
  if(dimensions.page>dimensions.viewport+1)throw Error('English mobile horizontal overflow '+route+' '+JSON.stringify(dimensions));
  const routeFailures=requests.slice(requestStart).filter(r=>r.status>=500&&!(r.path==='/api/v1/mail/queue'&&r.status===503));
  if(routeFailures.length||errors.length!==errorStart)throw Error('English mobile request/runtime failure '+route+' '+JSON.stringify(routeFailures));
  if(englishScreenshotRoutes.has(route)){await page.screenshot({path:evidence+'/english-mobile-'+route.slice(1)+'.png',fullPage:true});englishMobileAria.push({route,snapshot:await page.locator('main').first().ariaSnapshot()})}
  englishMobileChecks.push({route,title:englishCatalogue[titleKey],locale:'en',viewport:375,documentWidth:dimensions.page,rawTranslationKeys,authenticated:true});
 }
 await page.getByRole('button',{name:'Open menu',exact:true}).click();
 const chineseCatalogueResponse=page.waitForResponse(r=>r.url()===base+'/api/v1/i18n/zh-CN'&&r.status()===200);
 await page.getByRole('button',{name:'中文',exact:true}).click();
 const chineseCatalogue=await(await chineseCatalogueResponse).json();
 await page.getByRole('button',{name:'English',exact:true}).waitFor();
 await page.keyboard.press('Escape');await page.setViewportSize({width:1440,height:1000});
 await page.getByRole('button',{name:chineseCatalogue['layout.open_menu'],exact:true}).waitFor({state:'hidden'});
 const bilingualMobile={englishCatalogueLoaded:true,englishCatalogueKeys:Object.keys(englishCatalogue).length,checks:englishMobileChecks,ariaSnapshots:englishMobileAria,chineseRestored:true};


 const suffix=String(Date.now()), ticketSubject='Browser support closure '+suffix, staffReply='Verified platform reply '+suffix;
 await page.goto(base+'/support');await page.waitForLoadState('networkidle');
 const ticketForm=page.locator('form').filter({has:page.locator('input[name="Subject"]')});await ticketForm.locator('input[name="Subject"]').fill(ticketSubject);await ticketForm.locator('textarea[name="Body"]').fill('Real browser ticket requiring a platform reply');
 const ticketCreated=page.waitForResponse(r=>r.url().endsWith('/api/v1/support/tickets')&&r.request().method()==='POST');await ticketForm.locator('button').click();const ticketResponse=await ticketCreated;if(ticketResponse.status()!==201)throw Error('Ticket creation failed '+ticketResponse.status());const ticket=(await ticketResponse.json()).ticket;
 await login(fixture.platform_account);await page.goto(base+'/platform/support');await page.waitForLoadState('networkidle');const staffTicket=page.locator('li').filter({hasText:ticketSubject});await staffTicket.locator('button').click();const replyForm=page.getByRole('dialog').locator('form').filter({has:page.locator('textarea[name="body"]')});await replyForm.locator('textarea[name="body"]').fill(staffReply);
 const replied=page.waitForResponse(r=>r.url().endsWith(`/tickets/${ticket.id}/messages`)&&r.request().method()==='POST');await replyForm.locator('button').click();if((await replied).status()!==201)throw Error('Platform reply failed');
 await login(account);await page.goto(base+'/support');await page.waitForLoadState('networkidle');await page.locator('li').filter({hasText:ticketSubject}).locator('button').click();await page.getByText(staffReply,{exact:true}).waitFor();await page.screenshot({path:evidence+'/support-reply-desktop.png',fullPage:true});await page.goto(base+'/notifications');await page.waitForLoadState('networkidle');const notices=await portalGet('/api/v1/notifications');if(!notices.notifications.some(n=>n.message_key==='notification.support_reply'&&n.params.ticket===ticket.id&&n.state==='delivered'))throw Error('Staff reply notification missing');
 const notificationLocales=[];
 for(const locale of ['zh-CN','en']){
  if(locale==='en'){
   const loaded=page.waitForResponse(r=>r.url()===base+'/api/v1/i18n/en'&&r.status()===200);await page.getByRole('button',{name:'English',exact:true}).click();await loaded;
  }
  const catalogue=locale==='en'?englishCatalogue:chineseCatalogue;
  const expectedNotification=catalogue['notification.support_reply'].replace(/\{+ticket\}+/g,String(ticket.id));
  await page.locator('main').getByText(expectedNotification,{exact:true}).waitFor();
  if(/\{+ticket\}+/.test(await page.locator('main').first().innerText()))throw Error('Support notification ticket id was not interpolated for '+locale);
  await page.setViewportSize({width:375,height:812});if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1))throw Error('Localized support notification overflow '+locale);await page.screenshot({path:evidence+'/support-notification-'+locale+'-mobile.png',fullPage:true});await page.setViewportSize({width:1440,height:1000});
  notificationLocales.push({locale,ticket_id:ticket.id,actualTicketRendered:true,noPlaceholder:true});
 }
 const notificationChineseRestored=page.waitForResponse(r=>r.url()===base+'/api/v1/i18n/zh-CN'&&r.status()===200);await page.getByRole('button',{name:'中文',exact:true}).click();await notificationChineseRestored;
 const ticketChain={ticket_id:ticket.id,platformReplyVisible:true,notificationDelivered:true,notificationLocales};
 await page.goto(base+'/marketing');await page.waitForLoadState('networkidle');const templateName='Browser template '+suffix,listName='Browser consent '+suffix,campaignName='Browser physical TLS '+suffix,subject='Verified local TLS marketing '+suffix,recipientEmail='physical-recipient-'+suffix+'@example.test';
 const templateForm=page.locator('form').filter({has:page.locator('textarea[name="text_body"]')});await templateForm.locator('input[name="name"]').fill(templateName);await templateForm.locator('input[name="subject"]').fill(subject);await templateForm.locator('textarea[name="text_body"]').fill('Explicitly consented local integration message');const templateSaved=page.waitForResponse(r=>r.url().endsWith('/api/v1/marketing/templates')&&r.request().method()==='POST');await templateForm.locator('button').click();if((await templateSaved).status()!==201)throw Error('Template UI failed');
 const listForm=page.locator('form:has(input[name="name"]):not(:has(select)):not(:has(textarea))');await listForm.locator('input[name="name"]').fill(listName);const listSaved=page.waitForResponse(r=>r.url().endsWith('/api/v1/marketing/lists')&&r.request().method()==='POST');await listForm.locator('button').click();const listResponse=await listSaved;if(listResponse.status()!==201)throw Error('List UI failed');const list=(await listResponse.json()).list;
 const contactForm=page.locator('form').filter({has:page.locator('textarea[name="emails"]')});await contactForm.locator('select[name="list_id"]').selectOption(String(list.id));await contactForm.locator('textarea[name="emails"]').fill(recipientEmail);await contactForm.locator('input[name="consent_source"]').fill('Explicit isolated browser test consent');await contactForm.locator('input[type="checkbox"]').check();const contactSaved=page.waitForResponse(r=>r.url().endsWith(`/lists/${list.id}/contacts`)&&r.request().method()==='POST');await contactForm.locator('button').click();if((await contactSaved).status()!==201)throw Error('Consent import UI failed');
 const campaignForm=page.locator('form').filter({has:page.locator('select[name="channel_id"]')});await campaignForm.locator('input[name="name"]').fill(campaignName);await campaignForm.locator('select[name="channel_id"]').selectOption(String(fixture.marketing.channel_id));await campaignForm.locator('select[name="template_id"]').selectOption({label:templateName});await campaignForm.locator('select[name="list_id"]').selectOption(String(list.id));const campaignSaved=page.waitForResponse(r=>r.url().endsWith('/api/v1/marketing/campaigns')&&r.request().method()==='POST');await campaignForm.locator('button').click();const campaignResponse=await campaignSaved;if(campaignResponse.status()!==201)throw Error('Campaign draft UI failed');const campaign=(await campaignResponse.json()).campaign;
 const campaignRow=page.locator('li').filter({hasText:campaignName});await campaignRow.locator('button').nth(1).click();const confirmation=page.getByRole('dialog').locator('form');const date=new Date(Date.now()-60000);const localSchedule=new Date(date.getTime()-date.getTimezoneOffset()*60000).toISOString().slice(0,16);await confirmation.locator('input[name="scheduled_at"]').fill(localSchedule);await confirmation.locator('input[type="checkbox"]').check();const confirmed=page.waitForResponse(r=>r.url().endsWith(`/campaigns/${campaign.id}/confirm`)&&r.request().method()==='POST');await confirmation.locator('button').click();if((await confirmed).status()!==200)throw Error('Campaign confirmation UI failed');
 let captured;for(let attempt=0;attempt<40;attempt++){const d=await(await page.request.get(fixture.marketing.mail_capture_url+'/messages')).json();captured=d.messages.find(m=>m.subject===subject);if(captured)break;await page.waitForTimeout(500)}if(!captured)throw Error('No physical TLS SMTP delivery');if(!captured.tls_verified_transport||captured.one_click!=='List-Unsubscribe=One-Click')throw Error('Physical message TLS/unsubscribe headers invalid');
 let recipients;for(let attempt=0;attempt<20;attempt++){recipients=(await portalGet(`/api/v1/marketing/campaigns/${campaign.id}/recipients`)).recipients;if(recipients.some(r=>r.status==='sent'))break;await page.waitForTimeout(200)}const recipient=recipients.find(r=>r.status==='sent');if(!recipient)throw Error('SMTP accepted message not durably sent');await page.reload();await page.waitForLoadState('networkidle');await page.locator('li').filter({hasText:campaignName}).locator('button').first().click();await page.getByText(recipientEmail,{exact:false}).waitFor();await page.screenshot({path:evidence+'/marketing-sent-desktop.png',fullPage:true});
 const beforeOptOut=(await portalGet('/api/v1/marketing/suppressions')).suppressions.length;if(!['127.0.0.1','localhost','::1'].includes(new URL(captured.unsubscribe_url).hostname))throw Error('Unsubscribe target must remain loopback');const tlsContext=await page.context().browser().newContext({ignoreHTTPSErrors:true});const unsubscribePage=await tlsContext.newPage();await unsubscribePage.goto(captured.unsubscribe_url);if((await portalGet('/api/v1/marketing/suppressions')).suppressions.length!==beforeOptOut)throw Error('GET unsubscribe mutated consent');await unsubscribePage.locator('form button[type="submit"]').click();await unsubscribePage.waitForLoadState('networkidle');const optedOut=(await portalGet('/api/v1/marketing/suppressions')).suppressions;if(!optedOut.some(r=>r.email===recipientEmail&&r.reason==='unsubscribe'))throw Error('Human POST did not persist unsubscribe');await unsubscribePage.screenshot({path:evidence+'/unsubscribe-confirmed.png',fullPage:true});await tlsContext.close();
 const feedbackBody=JSON.stringify({event_id:'browser-feedback-'+campaign.id,recipient_id:recipient.id,status:'complaint'}),timestamp=String(Math.floor(Date.now()/1000));const signature=await page.evaluate(async({body,timestamp,key})=>{const bytes=new TextEncoder();const imported=await crypto.subtle.importKey('raw',bytes.encode(key),{name:'HMAC',hash:'SHA-256'},false,['sign']);const signed=await crypto.subtle.sign('HMAC',imported,bytes.encode(timestamp+'.'+body));return [...new Uint8Array(signed)].map(v=>v.toString(16).padStart(2,'0')).join('')},{body:feedbackBody,timestamp,key:fixture.marketing.feedback_key});
 for(let repeat=0;repeat<2;repeat++){const response=await page.request.post(base+`/api/v1/marketing/channels/${fixture.marketing.channel_id}/feedback`,{data:feedbackBody,headers:{'Content-Type':'application/json','X-Urmail-Timestamp':timestamp,'X-Urmail-Signature':signature}});if(response.status()!==204)throw Error('Signed feedback/replay rejected '+response.status())}
 const finalCapture=(await(await page.request.get(fixture.marketing.mail_capture_url+'/messages')).json()).messages.filter(m=>m.subject===subject);if(finalCapture.length!==1)throw Error('Physical SMTP was duplicated');recipients=(await portalGet(`/api/v1/marketing/campaigns/${campaign.id}/recipients`)).recipients;if(!recipients.some(r=>r.id===recipient.id&&r.status==='bounced'&&r.result_code==='complaint'))throw Error('Feedback outcome not durable');const marketingChain={campaign_id:campaign.id,recipient_id:recipient.id,physicalTLSMessageSHA256:captured.sha256,physicalTLS:true,physicalMessageCount:1,durableSent:true,getUnsubscribeInert:true,humanUnsubscribePersisted:true,signedFeedbackReplayIdempotent:true,complaintDurable:true};
// Insert while the first tenant account is authenticated, before the platform switch.
const dnsFixture=fixture.dns;
await page.goto(base+'/domains');await page.waitForLoadState('networkidle');
await page.getByTestId('domain-row-'+dnsFixture.domain_id).getByRole('button',{name:'DNS 与开通',exact:true}).click();
const dnsPanel=page.getByTestId('domain-dns-panel');await dnsPanel.getByRole('heading',{name:'租户 DNS 凭据',exact:true}).waitFor();
await dnsPanel.getByLabel('精确 zone 名称',{exact:true}).fill(dnsFixture.zone);
await dnsPanel.getByLabel('API Token',{exact:true}).fill(dnsFixture.token);
const dnsSaved=page.waitForResponse(r=>r.url().endsWith('/dns/credentials')&&r.request().method()==='PUT');
await dnsPanel.getByRole('button',{name:'检查权限并加密保存',exact:true}).click();if((await dnsSaved).status()!==204)throw Error('DNS credential probe/save failed');
await page.waitForFunction(()=>[...document.querySelectorAll('input[type=password]')].every(element=>element.value===''));
if(await dnsPanel.getByLabel('API Token',{exact:true}).inputValue())throw Error('DNS token remained displayed after save');
await dnsPanel.locator('select').nth(1).selectOption('provider');
const getDNSState=()=>page.evaluate(async(url)=>{const r=await fetch(url);return r.json()},dnsFixture.state_url);
const beforeDNS=await getDNSState();
const previewDNS=page.waitForResponse(r=>r.url().endsWith('/dns/plans')&&r.request().method()==='POST');await dnsPanel.getByRole('button',{name:'只读生成差异预览',exact:true}).click();if((await previewDNS).status()!==201)throw Error('DNS preview failed');
if((await getDNSState()).writes!==beforeDNS.writes)throw Error('DNS preview performed a write');
if(!(await dnsPanel.getByRole('button',{name:'确认此计划',exact:true}).isDisabled()))throw Error('DNS plan skipped explicit confirmation');
await page.evaluate(async(url)=>{await fetch(url)},dnsFixture.unknown_url);
await dnsPanel.getByRole('checkbox').check();await dnsPanel.getByRole('button',{name:'确认此计划',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();
await dnsPanel.locator('summary').filter({hasText:'结果未知，待核对'}).waitFor({timeout:30000});
await page.setViewportSize({width:1440,height:1000});await page.screenshot({path:evidence+'/dns-uncertain-desktop.png',fullPage:true});
const unknownDNS=await getDNSState();await page.waitForTimeout(250);if((await getDNSState()).writes!==unknownDNS.writes)throw Error('Uncertain DNS result replayed');
await dnsPanel.getByRole('button',{name:'停止旧计划并保留待核事实',exact:true}).click();await dnsPanel.getByLabel('核对与操作理由（至少 10 个字符）',{exact:true}).fill('Browser explicitly reviewed the partial unknown provider result');await dnsPanel.getByRole('button',{name:'继续',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();
await dnsPanel.getByRole('button',{name:'只读生成差异预览',exact:true}).click();await dnsPanel.getByRole('checkbox').check();await dnsPanel.getByRole('button',{name:'确认此计划',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();await dnsPanel.locator('summary').filter({hasText:'已完成'}).first().waitFor({timeout:30000});await dnsPanel.locator('summary').filter({hasText:'已完成'}).first().click();await dnsPanel.getByRole('button',{name:'预备回滚自有变更',exact:true}).waitFor({timeout:30000});
await dnsPanel.getByRole('button',{name:'预备回滚自有变更',exact:true}).click();await dnsPanel.getByLabel('核对与操作理由（至少 10 个字符）',{exact:true}).fill('Browser requests guarded rollback preserving the existing website');await dnsPanel.getByRole('button',{name:'继续',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();await dnsPanel.locator('summary').filter({hasText:'已回滚'}).waitFor({timeout:30000});
const afterDNS=await getDNSState();if(!afterDNS.records.some(r=>r.id==='website'&&r.content==='192.0.2.55'))throw Error('Rollback changed existing website');
await dnsPanel.getByRole('button',{name:'核对后重新开通',exact:true}).click();await dnsPanel.getByLabel('核对与操作理由（至少 10 个字符）',{exact:true}).fill('Browser reviewed current DNS and requests a new real provisioning attempt');await dnsPanel.getByRole('button',{name:'继续',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();
await page.setViewportSize({width:375,height:812});if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1))throw Error('DNS mobile overflow');await page.screenshot({path:evidence+'/dns-mobile.png',fullPage:true});await page.setViewportSize({width:1440,height:1000});

 for(const route of ['/domains','/mailboxes','/aliases','/billing','/marketing','/support','/notifications','/settings/mfa','/ai','/archive','/delivery']){
  await page.goto(base+route);await page.waitForLoadState('networkidle');
  if(page.url().endsWith('/login'))throw Error('Lost authenticated route '+route);
  const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1);if(overflow)throw Error('Desktop overflow '+route);
 }
 await page.goto(base+'/settings/mfa');await page.waitForLoadState('networkidle');
 const timezone=page.locator('input[placeholder="Asia/Shanghai"]');const timezoneValue=(await timezone.inputValue())==='America/New_York'?'Asia/Tokyo':'America/New_York';await timezone.fill(timezoneValue);const settingsForm=timezone.locator('xpath=ancestor::form');
 const saved=page.waitForResponse(r=>r.url().endsWith('/api/v1/tenants/current')&&r.request().method()==='PATCH');await settingsForm.locator('button[type="submit"]').click();if((await saved).status()!==200)throw Error('Timezone save failed');
 const current=await page.evaluate(async()=>{const r=await fetch('/api/v1/tenants/current',{headers:{Authorization:'Bearer '+sessionStorage.getItem('urmail.session-token')}});return r.json()});if(current.settings.business_timezone!==timezoneValue)throw Error('Timezone not persisted');
 await page.setViewportSize({width:390,height:844});await page.goto(base+'/notifications');await page.waitForLoadState('networkidle');if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1))throw Error('Mobile overflow');await page.screenshot({path:evidence+'/notifications-mobile.png',fullPage:true});
 await page.setViewportSize({width:1440,height:1000});
 await login(fixture.platform_account);await page.goto(base+'/platform');await page.waitForLoadState('networkidle');
 if(page.url().endsWith('/login'))throw Error('Platform session failed');
 const must=['/api/v1/platform/payment-merchants','/api/v1/platform/payment-reviews','/api/v1/platform/refunds','/api/v1/platform/alerts','/api/v1/platform/billing-document-profile'];
 for(const path of must)if(!requests.some(r=>r.path===path&&r.status===200))throw Error('Platform endpoint not rendered '+path);

 const aiPanel=page.getByTestId('platform-ai-admin');await aiPanel.getByRole('combobox').first().selectOption(String(account.tenant_id));await aiPanel.locator('input[name="api_key"]').waitFor();
 if(!(await aiPanel.getByRole('button',{name:'仅检查已保存目标健康',exact:true}).isDisabled()))throw Error('Cloud health enabled without tenant consent');
 const aiForm=aiPanel.locator('form').filter({has:page.locator('input[name="api_key"]')});await aiForm.locator('input[name="model"]').fill('browser-model');await aiForm.locator('input[name="endpoint"]').fill('https://models.example.test/v1');await aiForm.locator('input[name="api_key"]').fill(fixture.ai_api_key);await aiForm.locator('select[name="currency"]').selectOption('USD');await aiForm.locator('input[name="input_rate"]').fill('1000000');await aiForm.locator('input[name="output_rate"]').fill('2000000');await aiForm.locator('input[type="checkbox"][required]').check();
 const aiSaved=page.waitForResponse(r=>r.url().endsWith(`/platform/tenants/${account.tenant_id}/ai/configuration`)&&r.request().method()==='POST');await aiForm.locator('button').click();if((await aiSaved).status()!==204)throw Error('Platform disabled model config save failed');await aiPanel.locator('input[name="api_key"]').waitFor();await page.waitForFunction(()=>document.querySelector('[data-testid="platform-ai-admin"] input[name="api_key"]')?.value==='');
 const aiMetadata=await portalGet(`/api/v1/platform/tenants/${account.tenant_id}/ai/configuration`);if(aiMetadata.tenant_consent.allowed||!aiMetadata.tenant_consent.tenant_local_only||aiMetadata.configuration.enabled)throw Error('Platform config overrode tenant policy');if(JSON.stringify(aiMetadata).includes(fixture.ai_api_key)||'api_key' in aiMetadata.configuration||'encrypted_key' in aiMetadata.configuration)throw Error('Platform credentials leaked through metadata');
 await aiPanel.getByRole('button',{name:'核对用量事实',exact:true}).click();const usageForm=aiPanel.locator('form').filter({has:page.locator('textarea[name="reason"]')});await usageForm.locator('select[name="status"]').selectOption('released');await usageForm.locator('select[name="currency"]').selectOption('USD');await usageForm.locator('input[name="tokens"]').fill('0');await usageForm.locator('input[name="cost_micros"]').fill('0');await usageForm.locator('textarea[name="reason"]').fill('Local harness confirmed no model completion was ever requested');await usageForm.locator('input[type="checkbox"][required]').check();await aiPanel.screenshot({path:evidence+'/platform-ai-admin-desktop.png'});
 const usageResolved=page.waitForResponse(r=>r.url().endsWith(`/ai/usage/${fixture.ai_usage_id}/resolve`)&&r.request().method()==='POST');await usageForm.locator('button').click();const resolvedResponse=await usageResolved;if(resolvedResponse.status()!==200)throw Error('Platform usage zero release failed '+resolvedResponse.status());const resolvedBody=await resolvedResponse.json();if('result' in resolvedBody.request||'request_key' in resolvedBody.request||'input_digest' in resolvedBody.request)throw Error('Usage reconciliation exposed private model payload');if((await portalGet(`/api/v1/platform/tenants/${account.tenant_id}/ai/usage`)).requests.length!==0)throw Error('Resolved usage remained pending');
 const aiAdminChain={disabledConfigurationSaved:true,writeOnlyCredentialCleared:true,tenantConsentNotGranted:true,tenantLocalOnlyPreserved:true,noHealthOrCompletionRequested:!requests.some(r=>r.path.endsWith('/ai/health')||r.path.endsWith('/mailbox/ai')),explicitZeroUsageReleased:true,privateResultNotReturned:true};
 await page.screenshot({path:evidence+'/platform-desktop.png',fullPage:true});
 await page.goto(base+'/platform/support');await page.waitForLoadState('networkidle');
 await login(account);await page.goto(base+'/billing');await page.waitForLoadState('networkidle');
 const previousBilling=await portalGet('/api/v1/billing/subscription');
 await page.getByLabel(chineseCatalogue['page.billing.coupon'],{exact:true}).fill(fixture.billing.coupon_code);
 const planCard=page.getByText(fixture.billing.plan_name,{exact:true}).last().locator('xpath=..');
 const quoted=page.waitForResponse(r=>r.url().endsWith('/api/v1/billing/quote')&&r.request().method()==='POST');
 await planCard.getByRole('button',{name:chineseCatalogue['page.billing.select'],exact:true}).click();
 const quotedResponse=await quoted;if(quotedResponse.status()!==200)throw Error('Financial GUI server quote failed '+quotedResponse.status());
 const signedQuote=await quotedResponse.json();
 if(signedQuote.final_amount_cents!==0||signedQuote.base_amount_cents!==100||signedQuote.discount_cents!==100||signedQuote.coupon_code!==fixture.billing.coupon_code)throw Error('Financial GUI did not use the real full-discount quote');
 const originalPurchase=await page.evaluate(tid=>JSON.parse(sessionStorage.getItem('urmail.purchase.'+tid)||'null'),account.tenant_id);
 if(!originalPurchase||originalPurchase.quote.digest!==signedQuote.digest)throw Error('Quoted purchase intent was not persisted before checkout');
 await page.reload();await page.waitForLoadState('networkidle');
 const restoredPurchase=await page.evaluate(tid=>JSON.parse(sessionStorage.getItem('urmail.purchase.'+tid)||'null'),account.tenant_id);
 if(restoredPurchase?.key!==originalPurchase.key||restoredPurchase?.quote.digest!==signedQuote.digest)throw Error('Financial GUI refresh changed purchase key or signed quote');
 const zeroCheckout=page.waitForResponse(r=>r.url().endsWith('/api/v1/billing/checkout')&&r.request().method()==='POST');
 await page.getByRole('button',{name:chineseCatalogue['page.billing.confirm_payment'],exact:true}).click();
 const zeroResponse=await zeroCheckout,zeroResult=await zeroResponse.json();
 if(zeroResponse.status()!==201||zeroResult.order?.status!=='paid'||zeroResult.order?.amount_cents!==0||zeroResponse.request().headers()['idempotency-key']!==originalPurchase.key)throw Error('Financial GUI did not settle one zero-cost order with its original key');
 const paidMessage=chineseCatalogue['page.billing.completed_order'].replace('{{number}}',zeroResult.order.order_no);
 await page.getByRole('dialog').getByText(paidMessage,{exact:true}).waitFor();
 const paidDialogText=await page.getByRole('dialog').innerText();if(!paidDialogText.includes(zeroResult.order.order_no)||/\{+\s*number\s*\}+/.test(paidDialogText))throw Error('Completed order number was not interpolated');
 if(await page.getByRole('dialog').getByText(chineseCatalogue['page.billing.manual_order'].replace('{{number}}',zeroResult.order.order_no),{exact:true}).count())throw Error('Paid order still requests manual settlement');
 if(await page.evaluate(tid=>sessionStorage.getItem('urmail.purchase.'+tid),account.tenant_id)!==null)throw Error('Completed order left a resumable purchase intent');
 const updatedBilling=await portalGet('/api/v1/billing/subscription');
 if(Date.parse(updatedBilling.subscription.period_end)<=Date.parse(previousBilling.subscription.period_end)||Date.parse(signedQuote.period_start)<Date.parse(previousBilling.subscription.period_end))throw Error('Financial GUI renewal did not preserve and extend the existing paid period');
 await page.setViewportSize({width:375,height:812});await page.screenshot({path:evidence+'/billing-zero-paid-mobile.png',fullPage:true});
 await page.getByRole('dialog').getByRole('button',{name:chineseCatalogue['action.close'],exact:true}).first().click();
 await page.getByText(zeroResult.order.order_no,{exact:false}).first().waitFor();
 const latestInvoices=(await portalGet('/api/v1/billing/invoices')).invoices;const zeroInvoice=latestInvoices.find(i=>i.amount_cents===0&&Date.parse(i.period_start)===Date.parse(signedQuote.period_start));if(!zeroInvoice)throw Error('Zero order invoice missing');
 await page.getByText(zeroInvoice.number,{exact:true}).waitFor();
 if(await page.getByRole('button',{name:chineseCatalogue['page.billing.confirm_payment'],exact:true}).count())throw Error('Completed quote remains confirmable');
 if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1))throw Error('Completed billing mobile overflow');
 await page.setViewportSize({width:1440,height:1000});await page.screenshot({path:evidence+'/billing-zero-updated-desktop.png',fullPage:true});
 const zeroCheckoutRequests=requests.filter(r=>r.path==='/api/v1/billing/checkout'&&r.method==='POST');if(zeroCheckoutRequests.length!==1)throw Error('Financial GUI repeated checkout');
 const financialGUI={serverQuote:true,fullDiscount:true,refreshRestoredOriginalIntent:true,sameIdempotencyKey:true,orderID:zeroResult.order.id,paidNotice:true,purchaseIntentCleared:true,quoteCleared:true,subscriptionExtended:true,futureTermPreserved:true,invoiceVisible:true,lifecycleOrderVisible:true,checkoutRequests:zeroCheckoutRequests.length};
 await page.goto(fixture.quarantine_url+'/register');await page.waitForLoadState('networkidle');
 await page.getByRole('status').filter({hasText:'实例处于恢复隔离'}).waitFor();
 if(!(await page.locator('button[type="submit"]').isDisabled()))throw Error('Quarantined registration enabled');
 await page.screenshot({path:evidence+'/quarantine-registration.png',fullPage:true});

 const knownUnavailable=requests.filter(r=>r.path==='/api/v1/mail/queue'&&r.status===503);const failing=requests.filter(r=>r.status>=500&&!(r.path==='/api/v1/mail/queue'&&r.status===503));if(failing.length)throw Error('Unexpected server failures '+JSON.stringify(failing));if(errors.length)throw Error('Browser exceptions '+errors.join(';'));


 return {routes:16,bilingualMobile,financialGUI,aiAdminChain,ticketChain,marketingChain,dnsChain:{previewReadOnly:true,unknownWriteCount:unknownDNS.writes-beforeDNS.writes,uncertainNotReplayed:true,finalProviderWriteCount:afterDNS.writes,rollbackOwnChangesOnly:true,websitePreserved:true,onboardingRetryRequested:true},timezonePersisted:true,quarantineRegistrationDisabled:true,platformReads:must,knownUnavailable,errors,requests};
}
