use reqwest::Client;
use serde_json::Value;
use std::sync::Mutex;
use tauri::{path::BaseDirectory, Manager};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};
struct ApiClient(Client);
struct ClientSidecar(Mutex<Option<CommandChild>>);

#[tauri::command]
async fn api_request(
    client: tauri::State<'_, ApiClient>,
    base_url: String,
    path: String,
    method: String,
    body: Option<Value>,
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
                .args([config.to_string_lossy().to_string()])
                .current_dir(&config_dir)
                .spawn()?;
            tauri::async_runtime::spawn(async move {
                while let Some(event) = events.recv().await {
                    match event {
                        CommandEvent::Stdout(line) => {
                            println!("[client] {}", String::from_utf8_lossy(&line))
                        }
                        CommandEvent::Stderr(line) => {
                            eprintln!("[client] {}", String::from_utf8_lossy(&line))
                        }
                        _ => {}
                    }
                }
            });
            app.manage(ClientSidecar(Mutex::new(Some(child))));
            Ok(())
        })
        .on_window_event(|window, event| {
            if let tauri::WindowEvent::Destroyed = event {
                if let Some(state) = window.try_state::<ClientSidecar>() {
                    if let Ok(mut child) = state.0.lock() {
                        if let Some(process) = child.take() {
                            let _ = process.kill();
                        }
                    }
                }
            }
        })
        .invoke_handler(tauri::generate_handler![api_request])
        .run(tauri::generate_context!())
        .expect("error while running TunnelX");
}
