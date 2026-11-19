import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
const source=fs.readFileSync(new URL('../internal/web/static/termpane.js',import.meta.url),'utf8');
async function terminal(provider){
 const packets=[];let keyHandler,delegated=0;
 const element=()=>({appendChild(){},remove(){},offsetParent:true});
 class Terminal{constructor(){this.cols=80;this.rows=24}loadAddon(){}open(){}onData(){}onBinary(){}attachCustomKeyEventHandler(fn){keyHandler=fn}dispose(){}}
 class Socket{constructor(){this.readyState=1}send(data){if(typeof data!=='string')packets.push(new TextDecoder().decode(data))}close(){}}
 const window={innerWidth:1440,Terminal,FitAddon:{FitAddon:class{fit(){}}}};
 const ctx={window,document:{createElement:element,body:element(),hasFocus:()=>true},TextEncoder,Uint8Array,WebSocket:Socket,location:{protocol:'https:',host:'hub.example.ts.net'},fetch:()=>Promise.resolve({json:()=>Promise.resolve({provider})}),setInterval:()=>0,clearInterval(){},setTimeout:()=>0,clearTimeout(){}};
 vm.runInNewContext(source,ctx);
 new window.TermPane({id:1,status:'live',keys(){delegated++;return true}});
 await new Promise(setImmediate);
 return {packets,key:e=>keyHandler({type:'keydown',key:'Enter',ctrlKey:true,shiftKey:false,altKey:false,metaKey:false,preventDefault(){},...e}),delegated:()=>delegated};
}
test('Claude Control+Enter emits one extended key, not Enter and a timed chord',async()=>{const t=await terminal('claude');assert.equal(t.key({}),false);assert.deepEqual(t.packets,['\x1b[13;5u']);t.key({type:'keyup'});assert.equal(t.packets.length,1)});
for(const provider of ['shell','codex','unknown'])test(provider+' gets plain Enter without terminal flow-control bytes',async()=>{const t=await terminal(provider);t.key({});assert.deepEqual(t.packets,['\r'])});
test('Control+Shift+Enter remains available to workspace zoom',async()=>{const t=await terminal('claude');assert.equal(t.key({shiftKey:true}),true);assert.equal(t.delegated(),1);assert.deepEqual(t.packets,[])});
