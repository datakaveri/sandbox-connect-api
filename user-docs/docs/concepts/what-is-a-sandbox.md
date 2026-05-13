---
title: What is a Sandbox?
---

# What is a Sandbox?

A sandbox is a managed Jupyter notebook environment. CPU and GPU sandboxes run on Kubernetes through Kubeflow: users choose a category, reserve a time slot, and receive a notebook URL when the backing Kubernetes resources are ready.

The platform manages:

- user namespace/profile setup
- booking and slot validation
- notebook resource creation
- persistent storage
- start, stop, delete, and cleanup operations
- GPU role and credit checks where required

## CPU and GPU Sandboxes

CPU sandboxes are intended for lightweight notebook work and do not require compute credits in the default production profile.

GPU sandboxes are intended for accelerated workloads. GPU categories can require the `compute` role and profile credit eligibility before a booking is accepted.

## Jupyter Lite Free Mode

`jupyter_lite` is a free browser-only option. It runs JupyterLite in the user's browser with WebAssembly, so it does not create Kubernetes resources, consume compute credits, or use bookings and slots.

Files and settings are stored in browser-local storage. They are not backed up by Sandbox Connect, so users should download important notebooks when they need a copy outside that browser profile.
