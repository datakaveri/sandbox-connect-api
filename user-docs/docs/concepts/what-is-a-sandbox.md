---
title: What is a Sandbox?
---

# What is a Sandbox?

A sandbox is a managed Jupyter notebook environment running on Kubernetes through Kubeflow. Users choose a CPU or GPU category, reserve a time slot, and receive a notebook URL when the backing Kubernetes resources are ready.

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
