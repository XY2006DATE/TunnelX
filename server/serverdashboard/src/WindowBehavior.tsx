import {createContext, ReactNode, useContext, useEffect, useRef, useState} from "react";
import {createPortal} from "react-dom";
import {invoke} from "@tauri-apps/api/core";
import {listen} from "@tauri-apps/api/event";
import "./WindowBehavior.css";

type CloseBehavior = "ask" | "tray" | "exit";
type WindowSettings = {close_behavior: CloseBehavior; close_requested: boolean};
type PreferencesContext = {supported: boolean; behavior: CloseBehavior; busy: boolean; error: string;
  changeBehavior: (behavior: CloseBehavior) => Promise<void>};
const Preferences = createContext<PreferencesContext | null>(null);

export function WindowBehaviorProvider({children}: {children: ReactNode}) {
  const [supported, setSupported] = useState(false);
  const [behavior, setBehavior] = useState<CloseBehavior>("ask");
  const [pending, setPending] = useState(false);
  const [remember, setRemember] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dialog = useRef<HTMLDivElement>(null);
  const tr = (zh: string, en: string) => localStorage.getItem("tunnelx-language") === "en" ? en : zh;

  useEffect(() => {
    if (!("__TAURI_INTERNALS__" in window)) return;
    let disposed = false;
    let unlisten: (() => void) | undefined;
    async function start() {
      const stop = await listen("desktop-close-requested", () => {
        if (!disposed) {setSupported(true); setPending(true); setError("");}
      });
      if (disposed) {stop(); return;}
      unlisten = stop;
      // Subscribe before querying so a close during startup is not lost.
      const settings = await invoke<WindowSettings | null>("window_preferences");
      if (disposed || !settings) return;
      setSupported(true);
      setBehavior(settings.close_behavior);
      if (settings.close_requested) setPending(true);
    }
    void start().catch(e => {if (!disposed) setError(String(e));});
    return () => {disposed = true; unlisten?.();};
  }, []);

  useEffect(() => {
    if (!pending) return;
    const previous = document.activeElement as HTMLElement | null;
    dialog.current?.querySelector<HTMLButtonElement>("[data-default]")?.focus();
    return () => previous?.focus();
  }, [pending]);

  async function changeBehavior(next: CloseBehavior) {
    setBusy(true); setError("");
    try {await invoke("set_close_behavior", {behavior: next}); setBehavior(next);}
    catch (e) {setError(String(e));}
    finally {setBusy(false);}
  }

  async function resolve(decision: "tray" | "exit" | "cancel") {
    if (busy) return;
    setBusy(true); setError("");
    try {
      await invoke("resolve_close_request", {decision, remember});
      if (remember && decision !== "cancel") setBehavior(decision);
      setPending(false); setRemember(true);
    } catch (e) {setError(String(e));}
    finally {setBusy(false);}
  }

  return <Preferences.Provider value={{supported, behavior, busy, error, changeBehavior}}>
    {children}
    {pending && createPortal(<div className="desktop-close-backdrop">
      <div className="desktop-close-dialog" ref={dialog} role="dialog" aria-modal="true"
        aria-labelledby="desktop-close-title" aria-describedby="desktop-close-description"
        onKeyDown={event => {
          if (event.key === "Escape") {event.preventDefault(); void resolve("cancel");}
          if (event.key === "Tab") {
            const controls = dialog.current?.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled)");
            if (!controls?.length) return;
            const first = controls[0], last = controls[controls.length - 1];
            if (event.shiftKey && document.activeElement === first) {event.preventDefault(); last.focus();}
            else if (!event.shiftKey && document.activeElement === last) {event.preventDefault(); first.focus();}
          }
        }}>
        <span className="setting-kicker">TUNNELX</span>
        <h2 id="desktop-close-title">{tr("关闭窗口时如何处理？", "What should closing the window do?")}</h2>
        <p id="desktop-close-description">{tr("隐藏到系统托盘会继续运行隧道；退出程序会停止当前隧道和连接。", "Hiding to the system tray keeps tunnels running. Quitting stops the current tunnels and connections.")}</p>
        <label className="desktop-close-remember"><input type="checkbox" checked={remember} disabled={busy}
          onChange={event => setRemember(event.target.checked)}/>
          {tr("记住选择，可在设置中修改", "Remember my choice. You can change it in Settings.")}
        </label>
        {error && <p className="desktop-window-error" role="alert">{error}</p>}
        <div className="desktop-close-actions">
          <button type="button" disabled={busy} onClick={() => void resolve("cancel")}>{tr("取消", "Cancel")}</button>
          <button type="button" disabled={busy} onClick={() => void resolve("exit")}>{tr("退出程序", "Quit")}</button>
          <button type="button" data-default className="desktop-close-primary" disabled={busy}
            onClick={() => void resolve("tray")}>{tr("隐藏到托盘", "Hide to tray")}</button>
        </div>
      </div>
    </div>, document.body)}
  </Preferences.Provider>;
}

export function WindowBehaviorSetting({tr}: {tr: (zh: string, en: string) => string}) {
  const preferences = useContext(Preferences);
  if (!preferences?.supported) return null;
  return <section className="settings-card">
    <div><span className="setting-kicker">WINDOW</span>
      <h3>{tr("关闭窗口时", "When closing the window")}</h3>
      <p>{tr("隐藏到托盘会保留后台隧道，退出程序会停止隧道。修改后立即生效，并在下次启动时保留。", "Hiding keeps tunnels running; quitting stops them. Changes apply immediately and are saved for the next launch.")}</p>
    </div>
    <div className="desktop-window-control">
      <select aria-label={tr("关闭窗口时", "When closing the window")} value={preferences.behavior}
        disabled={preferences.busy} onChange={event => void preferences.changeBehavior(event.target.value as CloseBehavior)}>
        <option value="ask">{tr("每次询问", "Ask each time")}</option>
        <option value="tray">{tr("隐藏到系统托盘", "Hide to system tray")}</option>
        <option value="exit">{tr("退出程序", "Quit the application")}</option>
      </select>
      <small role="status">{preferences.busy ? tr("正在保存…", "Saving…") : tr("自动保存", "Saved automatically")}</small>
      {preferences.error && <small className="desktop-window-error" role="alert">{preferences.error}</small>}
    </div>
  </section>;
}
