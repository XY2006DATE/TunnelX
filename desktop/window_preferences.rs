use serde::{Deserialize, Serialize};
use std::{
    path::PathBuf,
    sync::{
        atomic::{AtomicBool, Ordering},
        Mutex,
    },
};
use tauri::{Emitter, Manager};

#[derive(Clone, Copy, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum CloseBehavior {
    #[default]
    Ask,
    Tray,
    Exit,
}

#[derive(Default, Deserialize, Serialize)]
#[serde(default)]
struct SavedPreferences {
    close_behavior: CloseBehavior,
}

pub struct WindowPreferences {
    path: PathBuf,
    behavior: Mutex<CloseBehavior>,
    close_requested: AtomicBool,
}

impl WindowPreferences {
    fn load(path: PathBuf) -> Result<Self, std::io::Error> {
        let preferences = match std::fs::read(&path) {
            Ok(data) => serde_json::from_slice::<SavedPreferences>(&data).unwrap_or_default(),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                SavedPreferences::default()
            }
            Err(error) => return Err(error),
        };
        Ok(Self {
            path,
            behavior: Mutex::new(preferences.close_behavior),
            close_requested: AtomicBool::new(false),
        })
    }

    fn save(&self, behavior: CloseBehavior) -> Result<(), String> {
        let mut current = self.behavior.lock().map_err(|e| e.to_string())?;
        let data = serde_json::to_vec_pretty(&SavedPreferences {
            close_behavior: behavior,
        })
        .map_err(|e| e.to_string())?;
        let temporary = self.path.with_extension("json.tmp");
        std::fs::write(&temporary, data).map_err(|e| e.to_string())?;
        std::fs::rename(&temporary, &self.path).map_err(|e| e.to_string())?;
        *current = behavior;
        Ok(())
    }
}

#[derive(Serialize)]
pub struct WindowSettings {
    close_behavior: CloseBehavior,
    close_requested: bool,
}

#[cfg(target_os = "windows")]
pub fn init(app: &tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    let directory = app.path().app_config_dir()?;
    std::fs::create_dir_all(&directory)?;
    app.manage(WindowPreferences::load(
        directory.join("window-preferences.json"),
    )?);
    Ok(())
}

#[tauri::command]
pub fn window_preferences(app: tauri::AppHandle) -> Result<Option<WindowSettings>, String> {
    let Some(preferences) = app.try_state::<WindowPreferences>() else {
        return Ok(None);
    };
    let close_behavior = *preferences.behavior.lock().map_err(|e| e.to_string())?;
    Ok(Some(WindowSettings {
        close_behavior,
        close_requested: preferences.close_requested.load(Ordering::SeqCst),
    }))
}

#[tauri::command]
pub fn set_close_behavior(app: tauri::AppHandle, behavior: CloseBehavior) -> Result<(), String> {
    let preferences = app
        .try_state::<WindowPreferences>()
        .ok_or("Window preferences are unavailable")?;
    preferences.save(behavior)
}

#[derive(Clone, Copy, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CloseDecision {
    Tray,
    Exit,
    Cancel,
}

fn remembered_behavior(decision: CloseDecision, remember: bool) -> Option<CloseBehavior> {
    match (decision, remember) {
        (CloseDecision::Tray, true) => Some(CloseBehavior::Tray),
        (CloseDecision::Exit, true) => Some(CloseBehavior::Exit),
        _ => None,
    }
}

#[tauri::command]
pub fn resolve_close_request(
    app: tauri::AppHandle,
    decision: CloseDecision,
    remember: bool,
) -> Result<(), String> {
    let preferences = app
        .try_state::<WindowPreferences>()
        .ok_or("Window preferences are unavailable")?;
    if !preferences.close_requested.load(Ordering::SeqCst) {
        return Err("No window close request is pending".into());
    }
    if let Some(behavior) = remembered_behavior(decision, remember) {
        preferences.save(behavior)?;
    }
    match decision {
        CloseDecision::Tray => {
            app.get_webview_window("main")
                .ok_or("Main window is unavailable")?
                .hide()
                .map_err(|e| e.to_string())?;
        }
        CloseDecision::Exit => app.exit(0),
        CloseDecision::Cancel => {}
    }
    preferences.close_requested.store(false, Ordering::SeqCst);
    Ok(())
}

#[cfg(target_os = "windows")]
pub fn handle_close(window: &tauri::Window) {
    let preferences = window.state::<WindowPreferences>();
    let behavior = *preferences
        .behavior
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    match behavior {
        CloseBehavior::Tray => {
            let _ = window.hide();
        }
        CloseBehavior::Exit => window.app_handle().exit(0),
        CloseBehavior::Ask => {
            // Retain this flag so a close during startup is shown after the UI subscribes.
            preferences.close_requested.store(true, Ordering::SeqCst);
            let _ = window.emit("desktop-close-requested", ());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temporary_directory() -> PathBuf {
        let directory = std::env::temp_dir().join(format!(
            "tunnelx-window-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir(&directory).unwrap();
        directory
    }

    #[test]
    fn first_run_asks_and_remembered_choices_survive_restarts() {
        let directory = temporary_directory();
        let path = directory.join("window-preferences.json");
        let preferences = WindowPreferences::load(path.clone()).unwrap();
        assert_eq!(*preferences.behavior.lock().unwrap(), CloseBehavior::Ask);
        for behavior in [CloseBehavior::Tray, CloseBehavior::Exit, CloseBehavior::Ask] {
            preferences.save(behavior).unwrap();
            let restarted = WindowPreferences::load(path.clone()).unwrap();
            assert_eq!(*restarted.behavior.lock().unwrap(), behavior);
            assert!(!restarted.close_requested.load(Ordering::SeqCst));
        }
        std::fs::remove_dir_all(directory).unwrap();
    }

    #[test]
    fn cancel_and_unremembered_choices_do_not_change_the_default() {
        assert_eq!(remembered_behavior(CloseDecision::Cancel, true), None);
        assert_eq!(remembered_behavior(CloseDecision::Tray, false), None);
        assert_eq!(remembered_behavior(CloseDecision::Exit, false), None);
        assert_eq!(
            remembered_behavior(CloseDecision::Tray, true),
            Some(CloseBehavior::Tray)
        );
        assert_eq!(
            remembered_behavior(CloseDecision::Exit, true),
            Some(CloseBehavior::Exit)
        );
    }

    #[test]
    fn unreadable_preferences_fail_without_changing_the_selected_behavior() {
        let directory = temporary_directory();
        let preferences =
            WindowPreferences::load(directory.join("missing").join("preferences.json")).unwrap();
        assert!(preferences.save(CloseBehavior::Exit).is_err());
        assert_eq!(*preferences.behavior.lock().unwrap(), CloseBehavior::Ask);
        std::fs::remove_dir_all(directory).unwrap();
    }

    #[test]
    fn invalid_preferences_fall_back_to_asking() {
        let directory = temporary_directory();
        let path = directory.join("window-preferences.json");
        std::fs::write(&path, br#"{"close_behavior":"unknown"}"#).unwrap();
        let preferences = WindowPreferences::load(path).unwrap();
        assert_eq!(*preferences.behavior.lock().unwrap(), CloseBehavior::Ask);
        std::fs::remove_dir_all(directory).unwrap();
    }
}
