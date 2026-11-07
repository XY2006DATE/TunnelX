export function parseServerEndpoint(raw: string) {
  const value = raw.trim();
  if (!value.includes("://")) throw new Error("请输入包含协议的完整服务地址");
  const parsed = new URL(value);
  const scheme = parsed.protocol.toLowerCase();
  if (!parsed.hostname || parsed.username || parsed.password || parsed.hash) throw new Error("服务端地址不能包含账号、密码或片段标记");
  if (!["http:", "https:", "ws:", "wss:", "tcp:", "tls:"].includes(scheme)) throw new Error("服务端地址支持 http、https、ws、wss、tcp 或 tls 协议");
  const webSocket = scheme === "ws:" || scheme === "wss:" ||
    ((scheme === "http:" || scheme === "https:") && ((parsed.pathname !== "" && parsed.pathname !== "/") || parsed.search !== ""));
  if (!webSocket && (scheme === "tcp:" || scheme === "tls:") && ((parsed.pathname !== "" && parsed.pathname !== "/") || parsed.search)) throw new Error("TCP/TLS 直连地址不能包含路径");
  const serverTLS = ["https:", "wss:", "tls:"].includes(scheme);
  const defaultPort = ["https:", "wss:"].includes(scheme) ? 443 : ["http:", "ws:"].includes(scheme) ? 80 : 7100;
  const serverPort = parsed.port ? Number(parsed.port) : defaultPort;
  if (serverPort < 1 || serverPort > 65535) throw new Error("控制端口必须在 1-65535 之间");
  if (webSocket) {
    parsed.protocol = serverTLS ? "wss:" : "ws:";
    if (!parsed.pathname || parsed.pathname === "/") parsed.pathname = "/proxy";
    return {serverAddr: parsed.toString(), serverPort, serverTLS};
  }
  return {serverAddr: parsed.hostname.replace(/^\[|\]$/g, "").toLowerCase(), serverPort, serverTLS};
}

export function serverDisplay(address: string, port?: number) {
  return /^wss?:\/\//.test(address) ? address : remoteAddress(address, port);
}

export function remoteAddress(address: string, port?: number) {
  if (!address || !port) return "—";
  if (/^wss?:\/\//.test(address)) address = new URL(address).hostname;
  const host = address.includes(":") && !address.startsWith("[") ? `[${address}]` : address;
  return `${host}:${port}`;
}
