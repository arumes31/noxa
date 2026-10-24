import test from "node:test";
import assert from "node:assert/strict";
import {Connection} from "../src/core/connection.js";
import {encodeFrame, Msg} from "../src/core/frame.js";

class Socket {
    static last;
    readyState=1;
    sent=[];
    constructor(){Socket.last=this;queueMicrotask(()=>this.onopen());}
    send(frame){this.sent.push(frame);}
    close(){this.readyState=3;this.onclose();}
}
test("resume probes are coalesced and a responsive socket stays connected",async()=>{
    const connection=await Connection.open("ws://test",Socket);
    const first=connection.probe(), second=connection.probe();
    assert.equal(first,second); assert.equal(Socket.last.sent.length,1);
    Socket.last.onmessage({data:encodeFrame(Msg.Pong,{})});
    await first;assert.equal(connection.open,true);
    connection.close();
});
test("a stale OPEN socket emits close once after a bounded probe",async t=>{
    t.mock.timers.enable({apis:["setTimeout"]});
    const connection=await Connection.open("ws://test",Socket);
    let closed=0;connection.addEventListener("close",()=>closed++);
    const probe=connection.probe();
    t.mock.timers.tick(5000);
    await assert.rejects(probe,/did not answer/);
    assert.equal(connection.open,false);assert.equal(closed,1);
    connection.close();assert.equal(closed,1);
});
test("closing a socket without pending requests notifies the reconnect owner",async()=>{
    const connection=await Connection.open("ws://test",Socket);
    let closed=0;connection.addEventListener("close",()=>closed++);
    connection.close();assert.equal(closed,1);
});
