use reqwest::{redirect::Policy, Client, Method};
use serde_json::Value;
use std::{sync::Mutex, time::Duration};
#[cfg(target_os = "windows")]
use tauri::tray::{MouseButton, MouseButtonState, TrayIconEvent};
#[cfg(any(target_os = "macos", target_os = "windows"))]
use tauri::{
    menu::{Menu, MenuItem},
    tray::TrayIconBuilder,
    AppHandle,
};
use tauri::{path::BaseDirectory, Manager, RunEvent};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

struct ApiClient(Client);
#[path = "../../../../desktop/window_preferences.rs"]
mod window_preferences;
struct ClientSidecar {
    child: Mutex<Option<CommandChild>>,
    dashboard: Mutex<Result<Option<u16>, String>>,
}

#[tauri::command]
fn dashboard_port(sidecar: tauri::State<'_, ClientSidecar>) -> Result<Option<u16>, String> {
    sidecar.dashboard.lock().unwrap().clone()
}

#[tauri::command]
async fn api_request(
    client: tauri::State<'_, ApiClient>,
    sidecar: tauri::State<'_, ClientSidecar>,
    path: String,
    method: String,
    body: Option<Value>,
) -> Result<Value, String> {
    let port = dashboard_port(sidecar)?.ok_or("TunnelX is starting")?;
    if !matches!(
        path.as_str(),
        "/api/setup-status"
            | "/api/setup"
            | "/login"
            | "/api/stats"
            | "/api/proxies"
            | "/api/requests"
            | "/api/request-proxy"
            | "/api/delete-proxy"
            | "/api/runtime-config"
            | "/api/password"
    ) {
        return Err("Unsupported dashboard endpoint".into());
    }
    let method = match method.as_str() {
        "GET" => Method::GET,
        "POST" => Method::POST,
        _ => return Err("Unsupported dashboard method".into()),
    };
    let mut request = client
        .0
        .request(method, format!("http://127.0.0.1:{port}{path}"));
    if let Some(payload) = body {
        request = request.json(&payload);
    }
    let response = request.send().await.map_err(|e| e.to_string())?;
    let status = response.status();
    let text = response.text().await.map_err(|e| e.to_string())?;
    if !status.is_success() {
        return Err(if text.is_empty() {
            status.to_string()
        } else {
            text.trim().to_owned()
        });
    }
    serde_json::from_str(&text).map_err(|e| format!("Invalid API response: {e}"))
}

#[cfg(any(target_os = "macos", target_os = "windows"))]
fn show_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        #[cfg(target_os = "macos")]
        let _ = app.set_dock_visibility(true);
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let client = Client::builder()
        .cookie_store(true)
        .no_proxy()
        .redirect(Policy::none())
        .timeout(Duration::from_secs(35))
        .build()
        .expect("http client");
    tauri::Builder::default()
        .manage(ApiClient(client))
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            #[cfg(target_os = "windows")]
            window_preferences::init(app)?;
            #[cfg(any(target_os = "macos", target_os = "windows"))]
            {
                #[cfg(target_os = "macos")]
                let labels = ("Open TunnelX", "Hide to Menu Bar", "Quit TunnelX");
                #[cfg(target_os = "windows")]
                let labels = ("打开 TunnelX", "隐藏到托盘", "退出 TunnelX");
                let show = MenuItem::with_id(app, "show", labels.0, true, None::<&str>)?;
                let hide = MenuItem::with_id(app, "hide", labels.1, true, None::<&str>)?;
                let quit = MenuItem::with_id(app, "quit", labels.2, true, None::<&str>)?;
                let menu = Menu::with_items(app, &[&show, &hide, &quit])?;
                let tray = TrayIconBuilder::with_id("tunnelx")
                    .icon(app.default_window_icon().expect("app icon").clone())
                    .tooltip("TunnelX")
                    .menu(&menu);
                #[cfg(target_os = "windows")]
                let tray = tray
                    .show_menu_on_left_click(false)
                    .on_tray_icon_event(|tray, event| {
                        if matches!(
                            event,
                            TrayIconEvent::Click {
                                button: MouseButton::Left,
                                button_state: MouseButtonState::Up,
                                ..
                            } | TrayIconEvent::DoubleClick {
                                button: MouseButton::Left,
                                ..
                            }
                        ) {
                            show_main_window(tray.app_handle());
                        }
                    });
                tray.on_menu_event(|app, event| match event.id().as_ref() {
                    "show" => show_main_window(app),
                    "hide" => {
                        if let Some(window) = app.get_webview_window("main") {
                            #[cfg(target_os = "macos")]
                            let _ = window.close();
                            #[cfg(target_os = "windows")]
                            let _ = window.hide();
                        }
                    }
                    "quit" => app.exit(0),
                    _ => {}
                })
                .build(app)?;
            }

            let bundled_config = app.path().resolve("client.yaml", BaseDirectory::Resource)?;
            let config_dir = app.path().app_config_dir()?;
            std::fs::create_dir_all(&config_dir)?;
            let config = config_dir.join("client.yaml");
            if !config.exists() {
                std::fs::copy(&bundled_config, &config)?;
            }
            let (mut events, child) = app
                .shell()
                .sidecar("tunnelx-client")?
                .args([config.as_os_str(), std::ffi::OsStr::new("--desktop")])
                .current_dir(&config_dir)
                .spawn()?;
            app.manage(ClientSidecar {
                child: Mutex::new(Some(child)),
                dashboard: Mutex::new(Ok(None)),
            });
            let handle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let mut startup_error = None;
                while let Some(event) = events.recv().await {
                    let state = handle.state::<ClientSidecar>();
                    match event {
                        CommandEvent::Stdout(line) => {
                            let line = String::from_utf8_lossy(&line);
                            if let Some(port) = line
                                .trim()
                                .strip_prefix("TUNNELX_DASHBOARD_READY ")
                                .and_then(|port| port.parse::<u16>().ok())
                                .filter(|port| *port > 0)
                            {
                                *state.dashboard.lock().unwrap() = Ok(Some(port));
                            } else {
                                println!("[client] {line}");
                            }
                        }
                        CommandEvent::Stderr(line) => {
                            let line = String::from_utf8_lossy(&line);
                            eprintln!("[client] {line}");
                            startup_error = Some(line.trim().to_owned());
                        }
                        CommandEvent::Terminated(_) => break,
                        _ => {}
                    }
                }
                let state = handle.state::<ClientSidecar>();
                *state.dashboard.lock().unwrap() = Err(startup_error
                    .unwrap_or_else(|| "TunnelX client stopped. Quit and reopen the app.".into()));
                state.child.lock().unwrap().take();
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            #[cfg(target_os = "windows")]
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                window_preferences::handle_close(window);
            }
            #[cfg(target_os = "macos")]
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                if window.hide().is_ok() {
                    let _ = window.app_handle().set_dock_visibility(false);
                }
            }

            #[cfg(not(any(target_os = "macos", target_os = "windows")))]
            let _ = (window, event);
        })
        .invoke_handler(tauri::generate_handler![
            api_request,
            dashboard_port,
            window_preferences::window_preferences,
            window_preferences::set_close_behavior,
            window_preferences::resolve_close_request
        ])
        .build(tauri::generate_context!())
        .expect("error while building TunnelX")
        .run(|app, event| match event {
            #[cfg(target_os = "macos")]
            RunEvent::Reopen { .. } => show_main_window(app),
            RunEvent::Exit => {
                if let Some(state) = app.try_state::<ClientSidecar>() {
                    if let Some(process) = state.child.lock().unwrap().take() {
                        let _ = process.kill();
                    }
                }
            }
            _ => {}
        });
}
