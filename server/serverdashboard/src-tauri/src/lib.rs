use reqwest::Client;
use serde_json::Value;
use std::{collections::HashMap, io, path::Path, sync::Mutex};
#[cfg(target_os = "windows")]
use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
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
struct ServerSidecar(Mutex<Option<CommandChild>>);
struct ServerEndpoint(String);

#[cfg(target_os = "windows")]
fn show_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

fn read_server_endpoint(config_path: &Path) -> io::Result<String> {
    let contents = std::fs::read_to_string(config_path)?;
    let mut section = "";
    let mut port = None;
    let mut tls_enabled = false;
    for line in contents.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        if !line.chars().next().is_some_and(char::is_whitespace) && trimmed.ends_with(':') {
            section = trimmed.trim_end_matches(':');
            continue;
        }
        let Some((key, raw_value)) = trimmed.split_once(':') else {
            continue;
        };
        let value = raw_value
            .split('#')
            .next()
            .unwrap_or_default()
            .trim()
            .trim_matches(['\'', '"']);
        match (section, key.trim()) {
            ("server", "bind_port") => {
                port = Some(value.parse::<u16>().map_err(|error| {
                    io::Error::new(io::ErrorKind::InvalidData, error.to_string())
                })?)
            }
            ("tls", "enable") => tls_enabled = value.eq_ignore_ascii_case("true"),
            _ => {}
        }
    }
    let port = port
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "server.bind_port is missing"))?;
    let scheme = if tls_enabled { "https" } else { "http" };
    Ok(format!("{}://127.0.0.1:{}", scheme, port))
}

#[tauri::command]
async fn login(
    client: tauri::State<'_, ApiClient>,
    endpoint: tauri::State<'_, ServerEndpoint>,
    base_url: String,
    password: String,
) -> Result<bool, String> {
    drop(base_url);
    let mut form = HashMap::new();
    form.insert("password", password);
    let response = client
        .0
        .post(format!("{}/login", endpoint.0))
        .form(&form)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if response.status().is_success() {
        Ok(true)
    } else {
        Err("管理员密码错误".into())
    }
}

#[tauri::command]
async fn api_request(
    client: tauri::State<'_, ApiClient>,
    endpoint: tauri::State<'_, ServerEndpoint>,
    base_url: String,
    path: String,
    method: String,
    body: Option<Value>,
    form: Option<HashMap<String, String>>,
) -> Result<Value, String> {
    drop(base_url);
    let url = format!("{}{}", endpoint.0, path);
    let mut request = if method == "POST" {
        client.0.post(url)
    } else {
        client.0.get(url)
    };
    if let Some(payload) = body {
        request = request.json(&payload);
    }
    if let Some(fields) = form {
        request = request.form(&fields);
    }
    let response = request.send().await.map_err(|e| e.to_string())?;
    let status = response.status();
    let text = response.text().await.map_err(|e| e.to_string())?;
    if !status.is_success() {
        return Err(if text.is_empty() {
            status.to_string()
        } else {
            text
        });
    }
    serde_json::from_str(&text).map_err(|e| format!("Invalid API response: {e}"))
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let client = Client::builder()
        .cookie_store(true)
        // The desktop wrapper only connects to its bundled sidecar over the
        // loopback interface. A public certificate normally names the server
        // domain rather than 127.0.0.1, so local certificate-name validation
        // must not prevent the administration window from loading.
        .danger_accept_invalid_certs(true)
        .build()
        .expect("http client");
    tauri::Builder::default()
        .manage(ApiClient(client))
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            #[cfg(target_os = "windows")]
            window_preferences::init(app)?;
            #[cfg(target_os = "windows")]
            {
                let show =
                    MenuItem::with_id(app, "show", "打开 TunnelX Server", true, None::<&str>)?;
                let hide = MenuItem::with_id(app, "hide", "隐藏到托盘", true, None::<&str>)?;
                let quit =
                    MenuItem::with_id(app, "quit", "退出 TunnelX Server", true, None::<&str>)?;
                let menu = Menu::with_items(app, &[&show, &hide, &quit])?;
                TrayIconBuilder::with_id("tunnelx-server")
                    .icon(app.default_window_icon().expect("app icon").clone())
                    .tooltip("TunnelX Server")
                    .menu(&menu)
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
                    })
                    .on_menu_event(|app, event| match event.id().as_ref() {
                        "show" => show_main_window(app),
                        "hide" => {
                            if let Some(window) = app.get_webview_window("main") {
                                let _ = window.hide();
                            }
                        }
                        "quit" => app.exit(0),
                        _ => {}
                    })
                    .build(app)?;
            }
            let bundled_config = app.path().resolve("server.yaml", BaseDirectory::Resource)?;
            let config_dir = app.path().app_config_dir()?;
            std::fs::create_dir_all(&config_dir)?;
            let config = config_dir.join("server.yaml");
            if !config.exists() {
                std::fs::copy(&bundled_config, &config)?;
            }
            app.manage(ServerEndpoint(read_server_endpoint(&config)?));

            let (mut events, child) = app
                .shell()
                .sidecar("tunnelx-server")?
                .args([config.to_string_lossy().to_string()])
                .current_dir(&config_dir)
                .spawn()?;
            tauri::async_runtime::spawn(async move {
                while let Some(event) = events.recv().await {
                    match event {
                        CommandEvent::Stdout(line) => {
                            println!("[server] {}", String::from_utf8_lossy(&line))
                        }
                        CommandEvent::Stderr(line) => {
                            eprintln!("[server] {}", String::from_utf8_lossy(&line))
                        }
                        _ => {}
                    }
                }
            });
            app.manage(ServerSidecar(Mutex::new(Some(child))));
            Ok(())
        })
        .on_window_event(|window, event| {
            #[cfg(target_os = "windows")]
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                window_preferences::handle_close(window);
            }
            #[cfg(not(target_os = "windows"))]
            let _ = (window, event);
        })
        .invoke_handler(tauri::generate_handler![
            login,
            api_request,
            window_preferences::window_preferences,
            window_preferences::set_close_behavior,
            window_preferences::resolve_close_request
        ])
        .build(tauri::generate_context!())
        .expect("error while building TunnelX Server")
        .run(|app, event| {
            if let RunEvent::Exit = event {
                if let Some(state) = app.try_state::<ServerSidecar>() {
                    if let Ok(mut child) = state.0.lock() {
                        if let Some(process) = child.take() {
                            let _ = process.kill();
                        }
                    }
                }
            }
        });
}
