# Notebook API

This document provides a high-level overview of the requirements and rules for using the notebook-related API endpoints. It is intended for developers and users who want to understand what is needed to create, start, stop, or delete a notebook, and why certain operations may fail.

## Creating a Notebook
- **You must be authenticated.**
- **Notebook name must be unique** for your account/namespace.
- **Notebook type** must be either `cpu` or `gpu`.
- **To create a GPU notebook, you must have the `compute` role.**
- **You cannot exceed your notebook limits, which are set by environment variables:**
  - Maximum running CPU notebooks: `${API_NOTEBOOK_MAX_RUNNING_CPU}`
  - Maximum running GPU notebooks: `${API_NOTEBOOK_MAX_RUNNING_GPU}`
  - Maximum total CPU notebooks: `${API_NOTEBOOK_MAX_TOTAL_CPU}`
  - Maximum total GPU notebooks: `${API_NOTEBOOK_MAX_TOTAL_GPU}`
  - The total number of notebooks (CPU + GPU) cannot exceed the sum of your allowed maximums.
- **You may not be able to create a notebook if another operation is in progress** for your account (for example, if you or another process is also creating or starting a notebook at the same time). In this case, you will get an error asking you to try again later.

## Starting a Notebook
- **You can only start a notebook that is in the 'applied' state** (i.e., it has been created and is ready to start).
- **You cannot start a notebook if you have already reached your running notebook limit** (see above for limits, which are set by environment variables).
- **You may not be able to start a notebook if another operation is in progress** for your account (due to row-level locking).

## Stopping a Notebook
- **You can only stop a notebook that is currently running (applied state).**
- Stopping a notebook is always allowed and does not count against your limits.

## Deleting a Notebook
- **You can delete a notebook if it is in the 'applied' (running) state or has failed.**
- Deleting a notebook will remove it from your account and also delete its resources in the system.
- If the notebook is running, it will be stopped and all associated storage will be cleaned up.
- You must be authenticated to delete a notebook.

## Listing and Checking Notebook Status
- **List Notebooks:**
  - You can view all notebooks you have created in your account, along with their current status (running, stopped, failed, etc.).
  - You can filter and sort the list as needed.
- **Check Notebook Status:**
  - You can check the current status of a specific notebook by name.
  - This helps you see if a notebook is running, stopped, or has failed.

## Concurrency & Locking
- **Only one create or start operation can be performed at a time per user.**
- If you try to create or start multiple notebooks at the same time, one of the operations will fail with a 'please try again' error. This is to ensure your notebook limits are always enforced correctly and to prevent race conditions.