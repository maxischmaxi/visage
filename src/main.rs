use blockhash::blockhash64;
use chromiumoxide::browser::{Browser, BrowserConfig, HeadlessMode};
use chromiumoxide::cdp::browser_protocol::page::CaptureScreenshotFormat;
use chromiumoxide::page::ScreenshotParams;
use futures::StreamExt;
use regex::Regex;
use serde::Deserialize;
use sha1::{Digest, Sha1};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use tokio::process::{Child, Command};
use walkdir::WalkDir;

#[derive(Debug, Deserialize)]
struct Config {
    base_url: String,
    start_command: String,
}

#[derive(Debug, Deserialize)]
struct Story {
    path: PathBuf,
    name: String,
    component_name: String,
}

impl std::fmt::Display for Story {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}, {}", self.path.display(), self.name)
    }
}

#[derive(Debug, Deserialize)]
enum RegressionTestStatus {
    Created,
    Failed,
    Passed,
    Skipped,
}

#[derive(Debug, Deserialize)]
struct RegressionTestResult {
    status: RegressionTestStatus,
    current_test: RegressionTest,
    expected_test: RegressionTest,
}

#[derive(Debug, Deserialize)]
struct RegressionTest {
    component: String,
    viewport: String,
    dom_hash: String,
    style_hash: String,
    visual_hash: String,
    timestamp: u64,
}

fn load_config<P: AsRef<Path>>(path: P) -> Result<Config, Box<dyn std::error::Error>> {
    let config_content = std::fs::read_to_string(path).unwrap_or_else(|error| {
        panic!("Failed to read config file {}", error);
    });
    let config: Config = serde_json::from_str(&config_content).unwrap_or_else(|error| {
        panic!("Failed to parse config file {}", error);
    });
    Ok(config)
}

fn file_exists<P: AsRef<Path>>(path: P) -> bool {
    path.as_ref().exists()
}

fn get_home_dir() -> std::io::Result<PathBuf> {
    if let Some(home) = std::env::var_os("HOME") {
        Ok(PathBuf::from(home))
    } else if let Some(home) = std::env::var_os("USERPROFILE") {
        Ok(PathBuf::from(home))
    } else {
        Err(std::io::Error::new(
            std::io::ErrorKind::NotFound,
            "Home directory not found",
        ))
    }
}

fn get_current_working_dir() -> std::io::Result<PathBuf> {
    std::env::current_dir()
}

async fn stop_storybook(mut child: Child) -> std::io::Result<()> {
    use nix::sys::signal::{kill, Signal};
    use nix::unistd::Pid;

    if let Some(id) = child.id() {
        kill(Pid::from_raw(id as i32), Signal::SIGTERM).unwrap_or_else(|error| {
            panic!("Failed to kill storybook process {}", error);
        });
    }

    let _ = child.wait().await;

    Ok(())
}

async fn start_storybook() -> std::io::Result<Child> {
    let child = Command::new("npm")
        .arg("start")
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap_or_else(|error| {
            panic!("Failed to start storybook {}", error);
        });
    Ok(child)
}

fn get_ignores<P: AsRef<Path>>(dir: P) -> Vec<String> {
    let mut ignores: Vec<String> = vec![];

    if file_exists(dir.as_ref().join(".gitignore")) {
        let content =
            std::fs::read_to_string(dir.as_ref().join(".gitignore")).unwrap_or_else(|error| {
                panic!("Failed to read .gitignore file {}", error);
            });
        for line in content.lines() {
            if !line.is_empty() && !line.starts_with('#') {
                ignores.push(line.to_string());
            }
        }
    }

    ignores
}

fn get_all_stories<P: AsRef<Path>>(dir: P) -> Vec<Story> {
    let ignores = get_ignores(dir.as_ref());

    let paths: Vec<String> = WalkDir::new(dir)
        .into_iter()
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_file())
        .filter(|e| {
            let name = e.file_name().to_string_lossy();
            name.ends_with(".stories.ts") || name.ends_with(".stories.tsx")
        })
        .filter(|e| {
            let path = e.path().to_string_lossy();
            for ignore in &ignores {
                let re = Regex::new(&format!(r"^{}$", ignore)).unwrap_or_else(|error| {
                    panic!("Failed to compile regex {}", error);
                });

                if re.is_match(&path) {
                    return false;
                }
            }

            true
        })
        .map(|e| e.path().to_string_lossy().into_owned())
        .collect();

    let mut stories: Vec<Story> = vec![];

    for path in paths {
        let content = std::fs::read_to_string(path.clone()).unwrap_or_else(|error| {
            panic!("Failed to read file {} {}", path, error);
        });

        let component_name = path
            .split("/")
            .last()
            .unwrap_or_default()
            .split('.')
            .next()
            .unwrap_or_default();

        let re = Regex::new(r#"export const (\w+): Story"#).unwrap_or_else(|error| {
            panic!("Failed to compile regex {}", error);
        });

        for (_, [hit]) in re.captures_iter(&content).map(|cap| cap.extract()) {
            let story = Story {
                path: PathBuf::from(path.clone()),
                name: hit.to_string(),
                component_name: component_name.to_string(),
            };

            stories.push(story);
        }
    }

    return stories;
}

async fn check_story(
    story: &Story,
    config: &Config,
    browser: &Browser,
) -> Result<RegressionTestResult, Box<dyn std::error::Error>> {
    let url = format!("{}/{}", config.base_url, story);
    let page = browser.new_page(url.clone()).await.unwrap_or_else(|error| {
        panic!("Failed to open page: {} {}", url.clone(), error);
    });
    page.wait_for_navigation().await.unwrap_or_else(|error| {
        panic!("Failed to navigate to page: {} {}", url.clone(), error);
    });

    let html = page.content().await.unwrap_or_else(|error| {
        panic!("Failed to get page content: {} {}", url.clone(), error);
    });
    let html_digest = md5::compute(html.as_bytes());
    let html_hash = format!("{:x}", html_digest);

    let mut hasher = Sha1::new();
    let style: String = page
        .evaluate("document.styleSheets[0].cssRules[0].cssText")
        .await
        .unwrap_or_else(|error| {
            panic!("Failed to get styles from page: {}, {}", url.clone(), error);
        })
        .into_value()
        .unwrap_or_else(|error| {
            panic!(
                "Failed to evaluate styles on page: {} {}",
                url.clone(),
                error
            );
        });

    assert!(style.len() > 0, "No styles found");

    hasher.update(style);

    let screenshot = page
        .screenshot(
            ScreenshotParams::builder()
                .format(CaptureScreenshotFormat::Png)
                .full_page(true)
                .omit_background(true)
                .build(),
        )
        .await
        .unwrap_or_else(|error| {
            panic!(
                "Failed to take screenshot of page: {}, {}",
                url.clone(),
                error
            );
        });

    let img = image::load_from_memory(&screenshot).unwrap_or_else(|error| {
        panic!("Failed to load image from screenshot {}", error);
    });
    let visual_hash = blockhash64(&img);

    let current_test = RegressionTest {
        viewport: String::from("1920x1080"),
        component: format!("{}.{}", story.name, story.path.display()),
        dom_hash: html_hash,
        style_hash: String::new(),
        visual_hash: visual_hash.to_string(),
        timestamp: 0,
    };

    let expected_test = RegressionTest {
        viewport: current_test.viewport.clone(),
        component: current_test.component.clone(),
        dom_hash: current_test.dom_hash.clone(),
        style_hash: current_test.style_hash.clone(),
        visual_hash: current_test.visual_hash.clone(),
        timestamp: 0,
    };

    let result = RegressionTestResult {
        status: RegressionTestStatus::Created,
        current_test,
        expected_test,
    };

    Ok(result)
}

async fn check(
    config: &Config,
    cwd: &Path,
) -> Result<Vec<RegressionTestResult>, Box<dyn std::error::Error>> {
    let stories = get_all_stories(cwd);

    println!("Found {} stories", stories.len());

    let package_json_path = cwd.join("package.json");
    if !file_exists(&package_json_path) {
        eprintln!("No package.json found in the current directory");
        std::process::exit(1);
    }

    let storybook = start_storybook().await.unwrap_or_else(|error| {
        panic!("Failed to start storybook {}", error);
    });

    let (browser, mut header) = Browser::launch(
        BrowserConfig::builder()
            .no_sandbox()
            .headless_mode(HeadlessMode::True)
            .build()
            .unwrap_or_else(|error| {
                panic!("Failed to launch browser {}", error);
            }),
    )
    .await
    .unwrap_or_else(|error| {
        panic!("Failed to launch browser {}", error);
    });

    let handle = tokio::task::spawn(async move {
        while let Some(h) = header.next().await {
            if h.is_err() {
                break;
            }
        }
    });

    let mut results: Vec<RegressionTestResult> = vec![];

    for story in &stories {
        let result = check_story(&story, config, &browser)
            .await
            .unwrap_or_else(|error| {
                panic!("Failed to check story: {} {}", story, error);
            });

        results.push(result);
    }

    handle.await.unwrap_or_else(|error| {
        panic!("Failed to wait for browser header {}", error);
    });

    stop_storybook(storybook).await.unwrap_or_else(|error| {
        panic!("Failed to stop storybook {}", error);
    });

    println!("Tests completed: {}", results.len());

    Ok(results)
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 2 {
        eprintln!("Usage: {} <path_to_regression_test>", args[0]);
        std::process::exit(1);
    }

    let cmd = String::from(&args[1]);
    let cwd = get_current_working_dir().unwrap_or_else(|error| {
        panic!("Failed to get current working directory {}", error);
    });
    let home_dir = get_home_dir().unwrap_or_else(|error| {
        panic!("Failed to get home directory {}", error);
    });

    let home_config_path = home_dir.join(".config").join("visage.json");
    let local_config_path = cwd.join("visage.json");

    println!("Current working directory: {:?}", cwd);
    println!("Home directory: {:?}", home_dir);
    println!("Local config path: {:?}", local_config_path);
    println!("Home config path: {:?}", home_config_path);

    let config_path = if local_config_path.exists() {
        local_config_path
    } else if home_config_path.exists() {
        home_config_path
    } else {
        eprintln!("No configuration file found");
        std::process::exit(1);
    };

    let config = load_config(config_path).unwrap_or_else(|error| {
        panic!("Failed to load configuration file {}", error);
    });

    match cmd.as_str() {
        "check" => {
            let mocked_file_path = home_dir
                .join("code")
                .join("tuv-galaxy")
                .join("component-library")
                .join("project");
            check(&config, &mocked_file_path)
                .await
                .unwrap_or_else(|error| {
                    panic!("Failed to check stories {}", error);
                })
                .into_iter()
                .for_each(|result| {
                    println!("Component: {}", result.current_test.component);
                    println!("Status: {:?}", result.status);
                    println!("Current Test: {:?}", result.current_test);
                    println!("Expected Test: {:?}", result.expected_test);
                });
        }
        _ => {
            eprintln!("Unknown command: {}", cmd);
            std::process::exit(1);
        }
    }

    Ok(())
}
