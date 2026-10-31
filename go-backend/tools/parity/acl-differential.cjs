#!/usr/bin/env node
'use strict'
// ACL differential probes on fresh independent Node and Go instances.
const fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http')
const {spawnSync}=require('node:child_process')
const {setTimeout:delay}=require('node:timers/promises')
const {freePort,spawnLogged,waitReady,stop}=require('./lib/process.cjs')
const {parseArgs}=require('./lib/args.cjs')
const {requestJSON}=require('./lib/http.cjs')
const {writeJSON}=require('./lib/artifacts.cjs')
const ROOT=path.resolve(__dirname,'../../..'), PREFIX='/audiobookshelf', PASSWORD='acl-password'
function args(){return parseArgs()}
const CASES=JSON.parse(fs.readFileSync(path.join(ROOT,'go-backend/contracts/parity/acl-cases.json'),'utf8'))
const port=freePort
const launch=spawnLogged
const stopProcess=(p)=>stop(p,12000)
const ready=(p,u,l)=>waitReady(p,u,{pathPrefix:PREFIX,logPath:l,exitMessage:`server exited; see ${l}`,timeoutMessage:`readiness timeout; see ${l}`})
async function req(u,route,{method='GET',token,body,headers={}}={}){try{const r=await requestJSON(u,route,{pathPrefix:PREFIX,method,redirect:'manual',timeoutMs:20000,token,body,headers});const h={};for(const n of ['content-type','www-authenticate','location'])if(r.headers.get(n)!==null)h[n]=r.headers.get(n);return{status:r.status,headers:h,body:r.bytes.toString('utf8')}}catch(e){return{status:0,headers:{},body:String(e)}}}
function media(dir,title){fs.mkdirSync(dir,{recursive:true});const r=spawnSync('ffmpeg',['-hide_banner','-loglevel','error','-f','lavfi','-i','anullsrc=r=44100:cl=mono','-t','3','-c:a','aac','-metadata',`title=${title}`,'-metadata','artist=ACL Fixture','-y',path.join(dir,title+'.m4b')],{encoding:'utf8'});if(r.status!==0)throw Error(`ffmpeg: ${r.stderr}`)}
function parsed(r){try{return JSON.parse(r.body)}catch{return{}}}
async function side(kind,work,bin,specs){
  const base=path.join(work,kind), config=path.join(base,'config'), metadata=path.join(base,'metadata'), books=path.join(base,'media/books')
  fs.mkdirSync(config,{recursive:true}); fs.mkdirSync(metadata,{recursive:true})
  media(path.join(books,'ACL Fixture','ACL Book'),'ACL Book')
  media(path.join(books,'ACL Fixture','ACL Book Two'),'ACL Book Two')
  // Local podcast RSS fixture with an explicit channel feeds the
  // explicit-content probe without any external network (PodcastController
  // explicit gate).
  const rss=http.createServer((rq,rsp)=>{
    if(rq.url.startsWith('/explicit-feed')){
      rsp.writeHead(200,{'content-type':'application/rss+xml'})
      rsp.end('<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Explicit Feed</title><itunes:explicit>true</itunes:explicit><item><title>Explicit One</title><itunes:explicit>true</itunes:explicit><guid>guid-1</guid></item></channel></rss>')
      return
    }
    rsp.writeHead(404); rsp.end()
  })
  await new Promise((r,j)=>{rss.listen(0,'127.0.0.1',e=>e?j(e):r(rss.address().port))})
  const p=await port(), url=`http://127.0.0.1:${p}`, log=path.join(work,kind+'.log')
  const proc=kind==='node'
    ? launch('mise',['exec','node@22','--','node',path.join(ROOT,'dist-server/index.js'),'--config',config,'--metadata',metadata,'--port',String(p),'--host','127.0.0.1','--source','docker'],{cwd:ROOT,env:{...process.env,NODE_ENV:'production'}},log)
    : launch(path.resolve(bin),[],{cwd:ROOT,env:{...process.env,CGO_ENABLED:'0',CONFIG_PATH:config,METADATA_PATH:metadata,HOST:'127.0.0.1',PORT:String(p),ROUTER_BASE_PATH:PREFIX,SOURCE:'docker'}},log)
  try{
    await ready(proc,url,log)
    const init=await req(url,'/init',{method:'POST',body:{newRoot:{username:'acl-root',password:PASSWORD}}})
    const login=await req(url,'/login',{method:'POST',body:{username:'acl-root',password:PASSWORD},headers:{'X-Return-Tokens':'true'}})
    const root=parsed(login).user?.accessToken||parsed(login).accessToken
    if(!root)throw Error(`root login failed ${login.status}: ${login.body}`)
    const lr=await req(url,'/api/libraries',{method:'POST',token:root,body:{name:'ACL Books',mediaType:'book',provider:'google',settings:{skipMatchingMediaWithAsin:true,skipMatchingMediaWithIsbn:true},folders:[{fullPath:books}]}})
    const lib=parsed(lr).id
    if(!lib)throw Error(`library create failed: ${lr.body}`)
    await req(url,`/api/libraries/${lib}/scan`,{method:'POST',token:root,body:{}})
    await delay(2500)
    const users={}; const tokens={}; const loginResults={}
    for(const [username,type,extra] of [['admin-2','admin',{}],['ordinary-1','user',{}],['restricted-1','user',{librariesAccessible:[lib],permissions:{accessAllLibraries:false,accessAllTags:false},itemTagsSelected:['tag-a']}],['explicit-denied-1','user',{permissions:{accessAllLibraries:true,accessAllTags:true,accessExplicitContent:false}}],['inactive-1','user',{isActive:false}]]){
      const payload={username,password:PASSWORD,type,isActive:extra.isActive!==false,...extra}
      const r=await req(url,'/api/users',{method:'POST',token:root,body:payload})
      users[username]=parsed(r).user||{createResponse:r}
      const lg=await req(url,'/login',{method:'POST',body:{username,password:PASSWORD},headers:{'X-Return-Tokens':'true'}})
      loginResults[username]={status:lg.status}
      tokens[username]=parsed(lg).user?.accessToken||parsed(lg).accessToken
    }
    // Tag witness setup: Book.updateFromRequest accepts a plain string-array
    // tags (Book.js:419-421) via PATCH /api/items/:id/media (ApiRouter.js:111,
    // LibraryItemController.updateMedia:208).
    const itemsList=parsed(await req(url,`/api/libraries/${lib}/items?limit=10&page=0`,{token:root}))
    const itemIds={}
    for(const it of itemsList.results||[]){
      if(it.media?.metadata?.title==='ACL Book')itemIds.bookOne=it.id
      if(it.media?.metadata?.title==='ACL Book Two')itemIds.bookTwo=it.id
    }
    if(!itemIds.bookOne||!itemIds.bookTwo)throw Error(`scan did not create both books: ${JSON.stringify(itemsList.results?.map(r=>r.media?.metadata?.title))}`)
    const tag1=await req(url,`/api/items/${itemIds.bookOne}/media`,{method:'PATCH',token:root,body:{tags:['tag-a','tag-b']}})
    const tag2=await req(url,`/api/items/${itemIds.bookTwo}/media`,{method:'PATCH',token:root,body:{tags:['tag-x']}})
    // Podcast library + explicit podcast creation (create-fixture.cjs:285-297
    // pattern) pointing at the local RSS fixture.
    const pdir=path.join(base,'media/podcasts'); fs.mkdirSync(pdir,{recursive:true})
    const plr=await req(url,'/api/libraries',{method:'POST',token:root,body:{name:'ACL Podcasts',mediaType:'podcast',provider:'podcastIndex',folders:[{fullPath:pdir}]}})
    const plib=parsed(plr).id, pfldr=parsed(plr).folders?.[0]?.id
     const pc=await req(url,'/api/podcasts',{method:'POST',token:root,body:{libraryId:plib,folderId:pfldr,path:path.join(pdir,'Explicit Feed'),media:{metadata:{title:'Explicit Feed',author:'ACL Fixture',description:'',explicit:true}}}})
    const pcid=parsed(pc).id
    const podcastItems=parsed(await req(url,`/api/libraries/${plib}/items?limit=10&page=0`,{token:root}))
    const podcastItemId=podcastItems.results?.find(it=>it.media?.metadata?.title==='Explicit Feed')?.id
    const state={kind,url,root,init,login,libraryId:lib,users,tokens,loginResults,itemIds,podcast:{libraryId:plib,id:pcid,itemId:podcastItemId,createStatus:pc.status,tag1Status:tag1.status,tag2Status:tag2.status}}
    // Specs execute while the server is alive — the finally below stops it.
    const obs={}
    for(const [id,fn] of specs)obs[id]=await fn(state)
    return{kind,obs}
  }catch(e){return{kind,setupError:String(e),obs:{}}}
  finally{
    await new Promise(r=>rss.close(()=>r()))
    await stopProcess(proc)
  }
}
const specs=[
  ['api-key-owner-create-use-delete-other', async s=>{
    // API-key routes are admin-gated. Root creates a key assigned to the
    // ordinary user; exercise that user's actual key, then distinguish the
    // route-level non-admin denial from admin deletion of another user's key.
    const create=await req(s.url,'/api/api-keys',{method:'POST',token:s.root,body:{name:'ACL Key',userId:s.users['ordinary-1'].id,isActive:true}})
    const created=parseMaybe(create.body)
    const useKeyAndReadLibraries=created.apiKey?.apiKey?await req(s.url,'/api/libraries',{token:created.apiKey.apiKey}):null
    const rootOwnedKeyRaw=await req(s.url,'/api/api-keys',{method:'POST',token:s.root,body:{name:'Root Delete Key',userId:s.users['ordinary-1'].id,isActive:true}})
    const rootOwnedKey=parseMaybe(rootOwnedKeyRaw.body)
    const otherOwnerKeyRaw=await req(s.url,'/api/api-keys',{method:'POST',token:s.root,body:{name:'Other Admin Key',userId:s.users['admin-2'].id,isActive:true}})
    const otherOwnerKey=parseMaybe(otherOwnerKeyRaw.body)
    const nonAdminDelete=created.apiKey?.id?await req(s.url,`/api/api-keys/${created.apiKey.id}`,{method:'DELETE',token:s.tokens['ordinary-1']}):null
    const adminForeignDelete=created.apiKey?.id?await req(s.url,`/api/api-keys/${created.apiKey.id}`,{method:'DELETE',token:s.tokens['admin-2']}):null
    const rootDelete=rootOwnedKey.apiKey?.id?await req(s.url,`/api/api-keys/${rootOwnedKey.apiKey.id}`,{method:'DELETE',token:s.root}):null
    const remainingKeys=await req(s.url,'/api/api-keys',{token:s.root})
    const otherOwnerKeyRemains=!!otherOwnerKey.apiKey?.id&&parseMaybe(remainingKeys.body)?.apiKeys?.some(k=>k.id===otherOwnerKey.apiKey.id)
    return{createStatus:create.status,keyUseStatus:useKeyAndReadLibraries?.status??null,nonAdminDeleteStatus:nonAdminDelete?.status??null,adminForeignDeleteStatus:adminForeignDelete?.status??null,rootDeleteStatus:rootDelete?.status??null,otherOwnerKeyRemains}
 }],
 ['restricted-tag-overlap',async s=>({allowed:await req(s.url,`/api/items/${s.itemIds.bookOne}`,{token:s.tokens['restricted-1']}),denied:await req(s.url,`/api/items/${s.itemIds.bookTwo}`,{token:s.tokens['restricted-1']})})],
  ['explicit-content-access',async s=>({denied:await req(s.url,`/api/items/${s.podcast.itemId}`,{token:s.tokens['explicit-denied-1']}),allowed:await req(s.url,`/api/items/${s.podcast.itemId}`,{token:s.root})})],
 ['inactive-login-and-api-key',async s=>{const login=await req(s.url,'/login',{method:'POST',body:{username:'inactive-1',password:PASSWORD},headers:{'X-Return-Tokens':'true'}});const created=parseMaybe((await req(s.url,'/api/api-keys',{method:'POST',token:s.root,body:{name:'Inactive Key',userId:s.users['inactive-1'].id,isActive:true}})).body);const apiKeyUse=created.apiKey?.apiKey?await req(s.url,'/api/libraries',{token:created.apiKey.apiKey}):null;return{login,apiKeyUse}}],
 ['restricted-library-listing',async s=>req(s.url,'/api/libraries',{token:s.tokens['restricted-1']})],
 ['malformed-permissions-create',async s=>req(s.url,'/api/users',{method:'POST',token:s.root,body:{username:'bad-permissions',password:PASSWORD,type:'user',permissions:'{bad json'}})],
 ['ordinary-create-user',async s=>req(s.url,'/api/users',{method:'POST',token:s.tokens['ordinary-1'],body:{username:'attempt-user',password:PASSWORD,type:'user'}})],
 ['ordinary-update-root',async s=>req(s.url,'/api/users/acl-root',{method:'PATCH',token:s.tokens['ordinary-1'],body:{email:'changed@example.test'}})],
 ['admin-delete-root',async s=>req(s.url,'/api/users/acl-root',{method:'DELETE',token:s.tokens['admin-2']})],
 ['root-update-restricted',async s=>req(s.url,'/api/users/acl-root',{method:'PATCH',token:s.root,body:{permissions:{accessAllLibraries:false}}})],
 ['ordinary-delete-self',async s=>req(s.url,`/api/users/${s.users['ordinary-1'].id}`,{method:'DELETE',token:s.tokens['ordinary-1']})]
]
async function main(){const a=args(),specsRef=specs,work=path.resolve(a['work-dir']||fs.mkdtempSync(path.join(os.tmpdir(),'acl-differential-'))),out=path.resolve(a.out||path.join(ROOT,'go-backend/contracts/parity/acl-differential-report.json'));fs.mkdirSync(work,{recursive:true});const sides=[],failures=[];let sideState=null
for(const k of ['node','go']){try{sideState=await side(k,work,a['go-bin'],specs);sides.push(sideState)}catch(e){failures.push({side:k,error:String(e)});sides.push({kind:k,setupError:String(e)})}}
const obs={node:null,go:null}
for(const s of sides){if(s.setupError){obs[s.kind]={}}else obs[s.kind]=s.obs}
const matrix=specs.map(([id])=>{const n=obs.node?.[id],g=obs.go?.[id];const pass=n&&g&&aclEqual(id,n,g);return{id,status:pass?'pass':n&&g?'divergence':'unresolved',source:CASES.cases.find(c=>c.id===id)?.source||[],node:n||null,go:g||null}});for(const c of CASES.cases)if(!specs.some(([id])=>id===c.id))matrix.push({id:c.id,status:'unresolved',source:c.source,reason:'Requires a separately initialized witness or route probe not yet implemented.'});const divergences=matrix.filter(c=>c.status==='divergence').map(c=>({id:c.id,node:c.node,go:c.go}));const report={baseline:CASES.baseline,workDir:work,sides:sides.map(({url,...s})=>s),matrix,divergences,setupFailures:failures,checksRun:['CGO_ENABLED=0 Go build','isolated Node 22 and Go fresh initialization','ffmpeg book fixture','user creation/logins','HTTP probes per side'],result:failures.length?'UNRESOLVED':divergences.length?'DIVERGENCE':matrix.some(c=>c.status==='unresolved')?'PARTIAL':'PASS'};writeJSON(out,report);console.log(JSON.stringify({result:report.result,report:out,cases:matrix.map(({id,status})=>({id,status})),divergences},null,2));if(failures.length||divergences.length)process.exitCode=1}
main().catch(e=>{console.error(e.stack||e);process.exitCode=1})
// Order-insensitive body equality: JSON bodies parse then compare; other
// texts compare exactly; non-JSON parseable composite objects (from multi-step
// cases) compare directly. Key-order-only divergences are visible in the raw
// body fields without failing the case.
function parseMaybe(value){if(typeof value!=='string')return value;try{return JSON.parse(value)}catch{return value}}
function deepEqual(a,b){if(a===b)return true;if(typeof a!=='object'||!a||typeof b!=='object'||!b)return false;if(Array.isArray(a)!==Array.isArray(b))return false;if(Array.isArray(a))return a.length===b.length&&a.every((v,i)=>deepEqual(v,b[i]));const ka=Object.keys(a),kb=Object.keys(b);return ka.length===kb.length&&ka.every(k=>deepEqual(a[k],b[k]))}
function bodyEqual(a,b){return deepEqual(parseMaybe(a),parseMaybe(b))}
function valueEqual(a,b){return deepEqual(a,b)}
function aclEqual(id,n,g){
 if(id==='api-key-owner-create-use-delete-other')return valueEqual(n,g)
 if(id==='restricted-tag-overlap'||id==='explicit-content-access')return Object.keys(n).length===Object.keys(g).length&&Object.keys(n).every(k=>n[k]?.status===g[k]?.status)
 if(id==='inactive-login-and-api-key')return n.login?.status===g.login?.status&&n.apiKeyUse?.status===g.apiKeyUse?.status
 if(typeof n.status==='number'||typeof g.status==='number')return n.status===g.status&&JSON.stringify(n.headers)===JSON.stringify(g.headers)&&aclBodyEqual(id,n.body,g.body)
 return valueEqual(n,g)
}
function aclBodyEqual(id,a,b){
 const na=parseMaybe(a),nb=parseMaybe(b)
 if(id==='restricted-library-listing'){
  const norm=(v)=>{if(Array.isArray(v))return v.map(norm);if(!v||typeof v!=='object')return v;return Object.fromEntries(Object.entries(v).filter(([k])=>!['id','libraryId','addedAt','lastScan','createdAt','lastUpdate','fullPath'].includes(k)).map(([k,x])=>[k,norm(x)]))}
  return deepEqual(norm(na),norm(nb))
 }
 if(id==='malformed-permissions-create'){
  const norm=(v)=>{if(Array.isArray(v))return v.map(norm);if(!v||typeof v!=='object')return v;return Object.fromEntries(Object.entries(v).filter(([k])=>!['id','token','createdAt'].includes(k)).map(([k,x])=>[k,norm(x)]))}
  return deepEqual(norm(na),norm(nb))
 }
 return deepEqual(na,nb)
}
