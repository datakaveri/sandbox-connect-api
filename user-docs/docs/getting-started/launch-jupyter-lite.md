---
title: Launch Jupyter Lite
---

# Launch Jupyter Lite

Jupyter Lite is the free browser-only sandbox mode. It runs in the user's browser with WebAssembly and stores notebooks/settings in browser-local storage.

## 1. List Categories

```bash
curl "$SANDBOX_API_URL/v1/categories" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Find the `jupyter_lite` category. It has `isBookable: false`, `resourceType: "browser"`, and a `launchUrl`.

## 2. Create a JupyterLite Launch Session

A browser navigation cannot attach an `Authorization` header by itself. Before opening the launch URL, the application should create a JupyterLite launch session using the logged-in user's bearer token:

```bash
curl -X POST "$SANDBOX_API_URL/v1/jupyterlite/session" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  --cookie-jar jupyterlite.cookies
```

The API validates the bearer token and sets an HttpOnly cookie for JupyterLite. The cookie is used only when the browser loads JupyterLite static files. KYC verification is not required for this free browser-only mode.

Frontend flow:

```js
await fetch(`${SANDBOX_API_URL}/v1/jupyterlite/session`, {
  method: "POST",
  headers: { Authorization: `Bearer ${accessToken}` },
  credentials: "include",
});
window.open(jupyterLiteLaunchUrl, "_blank", "noopener,noreferrer");
```

If a user opens the JupyterLite URL directly without this session cookie, the API returns `401 Unauthorized` with `Missing authorization header`.

## 3. Open the Launch URL

Open the returned `launchUrl`. The URL is usually:

```text
/jupyterlite/lab/index.html
```

Do not call slot, calendar, or booking endpoints for this category.

## 4. Open the Demo Notebook

The JupyterLite workspace includes a demo notebook named:

```text
jupyterlite-capabilities-demo.ipynb
```

Open it from the JupyterLite file browser and run the cells from top to bottom. It demonstrates browser Python execution, Markdown and code cells, standard-library modules, Pyodide-supported packages, plotting, reading a bundled CSV file, optional runtime package installation, browser-local persistence, and common limitations.

The notebook also includes Markdown-only guidance for upload and download workflows because those actions are performed through the JupyterLite UI.

## What You Can Do

Jupyter Lite opens a browser-based notebook environment. Users can:

- create, open, edit, rename, and delete notebooks from the JupyterLite file browser
- run Python code through the Pyodide kernel
- use code cells, Markdown cells, outputs, and normal notebook editing controls
- save notebooks and settings in browser-local storage
- upload files such as `.ipynb`, `.csv`, `.txt`, and other browser-supported files
- download notebooks and files back to the local machine
- read uploaded files from Python, such as loading a CSV with `pandas.read_csv("file.csv")`
- create basic plots with browser-compatible visualization libraries such as `matplotlib`
- use common Python workflows that fit inside the browser runtime

## Python Package Support

The Python kernel is powered by Pyodide, so package support is different from a full server-backed notebook.

Users can run:

- basic Python code
- many Python standard-library modules
- Pyodide-supported packages such as `numpy`, `pandas`, `matplotlib`, `scipy`, and other packages included in the active Pyodide distribution
- additional pure-Python packages installed at runtime with notebook `%pip install`
- packages that have Pyodide-compatible WebAssembly/Emscripten wheels

Runtime package installation is handled through JupyterLite's `piplite` layer on top of Pyodide `micropip`. For example:

```python
%pip install snowballstemmer
```

Packages that require native system libraries, unsupported binary wheels, local compilers, or OS-level services may not install or run in Jupyter Lite.

## Working With Files

Files in the JupyterLite file browser can be synchronized with the Pyodide kernel. A typical workflow is:

1. Upload `data.csv` into the JupyterLite file browser.
2. Open a notebook in the same workspace.
3. Read the file from Python:

```python
import pandas as pd

data = pd.read_csv("data.csv")
data.head()
```

Users can also fetch remote data from URLs when the browser and the remote server allow it. Browser security rules such as CORS still apply.

## Persistence

Jupyter Lite files are stored by the browser. They are not synced to Sandbox Connect storage, Kubernetes PVCs, or user profiles.

Because storage is browser-local:

- files usually stay available in the same browser profile on the same device
- files may not appear in a different browser, device, private/incognito session, or domain
- clearing site data, browser storage, or cache can remove notebooks and files
- important notebooks should be downloaded when users need a copy outside that browser profile

## Limitations

Jupyter Lite is useful for lightweight, zero-setup notebook work, but it is not the same as a CPU or GPU Sandbox Connect notebook.

It does not provide:

- Kubernetes-backed compute
- GPU access
- managed PVC storage
- Sandbox Connect storage sync
- terminal access
- system package installation
- native binaries or arbitrary OS access
- long-running backend processes
- booking lifecycle operations such as start, stop, cancel, extend, or terminate

Performance and memory depend on the user's browser and local device.
