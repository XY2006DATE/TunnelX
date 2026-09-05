use reqwest::Client;
use serde_json::Value;
use std::{collections::HashMap, sync::Mutex};
use tauri::{path::BaseDirectory, Manager};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

struct ApiClient(Client);
struct ServerSidecar(Mutex<Option<CommandChild>>);

#[tauri::command]
async fn login(
    client: tauri::State<'_, ApiClient>,
    base_url: String,
    password: String,
) -> Result<bool, String> {
    let mut form = HashMap::new();
    form.insert("password", password);
    let response = client
        .0
        .post(format!("{}/login", base_url.trim_end_matches('/')))
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
    base_url: String,
    path: String,
    method: String,
    body: Option<Value>,
    form: Option<HashMap<String, String>>,
) -> Result<Value, String> {
    let url = format!("{}{}", base_url.trim_end_matches('/'), path);
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
        .build()
        .expect("http client");
    tauri::Builder::default()
        .manage(ApiClient(client))
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            let bundled_config = app.path().resolve("server.yaml", BaseDirectory::Resource)?;
            let config_dir = app.path().app_config_dir()?;
            std::fs::create_dir_all(&config_dir)?;
            let config = config_dir.join("server.yaml");
            if !config.exists() {
                std::fs::copy(&bundled_config, &config)?;
            }

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
            if let tauri::WindowEvent::Destroyed = event {
                if let Some(state) = window.try_state::<ServerSidecar>() {
                    if let Ok(mut child) = state.0.lock() {
                        if let Some(process) = child.take() {
                            let _ = process.kill();
                        }
                    }
                }
            }
        })
        .invoke_handler(tauri::generate_handler![login, api_request])
        .run(tauri::generate_context!())
        .expect("error while running TunnelX Server");
}
