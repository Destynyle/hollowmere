// Hollowmere demo transport: the server runs in this page as WebAssembly.
// HollowmereTransport mimics the part of the WebSocket API that app.js uses
// (onopen, onmessage, onclose, send, close, readyState).
(() => {
  let resolveReady;
  const ready = new Promise((r) => { resolveReady = r; });
  window.hollowmereDemoReady = () => resolveReady();

  const go = new Go();
  const load = WebAssembly.instantiateStreaming
    ? WebAssembly.instantiateStreaming(fetch("hollowmere.wasm"), go.importObject)
    : fetch("hollowmere.wasm").then((r) => r.arrayBuffer()).then((b) => WebAssembly.instantiate(b, go.importObject));
  load.then((res) => go.run(res.instance)).catch((err) => {
    console.error(err);
    const banner = document.getElementById("demo-banner");
    if (banner) banner.textContent = "This browser could not start the game engine (WebAssembly): " + err.message;
  });

  class HollowmereTransport {
    constructor() {
      this.readyState = 0; // CONNECTING
      ready.then(() => {
        if (this.readyState === 3) return;
        this.conn = window.hollowmereDemoOpen(
          (line) => this.onmessage && this.onmessage({ data: line }),
          () => this.closed(),
        );
        this.readyState = 1; // OPEN
        if (this.onopen) this.onopen();
      });
    }
    send(line) { if (this.conn) this.conn.send(line); }
    close() {
      if (this.conn) this.conn.close();
      else this.closed();
    }
    closed() {
      if (this.readyState === 3) return;
      this.readyState = 3; // CLOSED
      if (this.onclose) this.onclose({ code: 1000, reason: "" });
    }
  }
  window.HollowmereTransport = HollowmereTransport;
})();
