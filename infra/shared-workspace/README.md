# Shared workspace storage (cluster admin setup)

The shared workspace gives every user profile one `ReadWriteMany` volume that an external copy
pod can populate and every notebook in that profile mounts **read-only** at
`/home/jovyan/workspace`. This page covers the cluster storage it needs and how to switch it on
and off. Configuration semantics live in the references:

- API: [`WORKSPACE_ENABLED`](../../docs/config/api.md#workspace_enabled) and
  [`API_WORKSPACE_STORAGE_CLASS` / `API_WORKSPACE_STORAGE_SIZE`](../../docs/config/api.md#api_workspace_storage_class-and-api_workspace_storage_size)
- Worker: [`WORKSPACE_ENABLED`](../../docs/config/worker.md#workspace_enabled) and the
  [template contract](../worker/README.md)

## How it works

1. When a notebook is created (or a profile is created), the API ensures a PVC named
   `workspace` exists in the user's profile namespace, using `API_WORKSPACE_STORAGE_CLASS`,
   `API_WORKSPACE_STORAGE_SIZE`, `ReadWriteMany` and `Filesystem`.
2. The worker selects the notebook template. With `WORKSPACE_ENABLED=true` it keeps the
   template's `shared-workspace` volume, which references claim `workspace`.
3. The template's `existing` volume policy makes the worker wait up to
   `lifecycle.externalPVCWaitTimeout` (for example `120s`) for that claim to be **Bound**. If it
   never binds, the worker logs `required PVC shared-workspace (workspace) was not available
   within …`, cleans up, and the notebook fails.

The storage class must therefore be able to **dynamically provision RWX volumes** before the
feature is enabled. A class that exists but has no working provisioner leaves every claim
`Pending` and breaks every notebook launch.

## Storage prerequisites

Any RWX-capable CSI driver works. Common choices:

| Platform | Driver | Example class |
|---|---|---|
| On-prem / Rook-Ceph | CephFS CSI | `ceph-filesystem` (the code default) |
| AWS EKS | Amazon EFS CSI driver | see [efs-storageclass.example.yaml](efs-storageclass.example.yaml) |

### AWS EKS: Amazon EFS CSI driver

Run these as a cluster/AWS administrator. Replace every `<placeholder>`.

**1. IAM role for the driver controller.** The controller creates EFS access points, so it needs
`AmazonEFSCSIDriverPolicy`, bound to service account `kube-system/efs-csi-controller-sa`.

Using EKS Pod Identity (requires the `eks-pod-identity-agent` add-on):

```bash
aws iam create-role --role-name <cluster>-efs-csi-driver \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"pods.eks.amazonaws.com"},"Action":["sts:AssumeRole","sts:TagSession"]}]}'
aws iam attach-role-policy --role-name <cluster>-efs-csi-driver \
  --policy-arn arn:aws:iam::aws:policy/service-role/AmazonEFSCSIDriverPolicy
aws eks create-pod-identity-association --cluster-name <cluster> --region <region> \
  --namespace kube-system --service-account efs-csi-controller-sa \
  --role-arn arn:aws:iam::<account-id>:role/<cluster>-efs-csi-driver
```

Or with IRSA via eksctl (requires the cluster OIDC provider):

```bash
eksctl create iamserviceaccount --cluster <cluster> --region <region> \
  --namespace kube-system --name efs-csi-controller-sa \
  --role-name <cluster>-efs-csi-driver --role-only \
  --attach-policy-arn arn:aws:iam::aws:policy/service-role/AmazonEFSCSIDriverPolicy --approve
```

**2. Install the driver as an EKS managed add-on.**

```bash
aws eks create-addon --cluster-name <cluster> --region <region> --addon-name aws-efs-csi-driver
# IRSA only: add --service-account-role-arn arn:aws:iam::<account-id>:role/<cluster>-efs-csi-driver
aws eks describe-addon --cluster-name <cluster> --region <region> \
  --addon-name aws-efs-csi-driver --query 'addon.status'
```

Wait for `ACTIVE`. Registering the `efs.csi.aws.com` CSIDriver object alone is not enough; the
controller Deployment and node DaemonSet must be running:

```bash
kubectl get deploy,ds -n kube-system | grep efs-csi
kubectl get pods -n kube-system -l app.kubernetes.io/name=aws-efs-csi-driver -o wide
```

Confirm an `efs-csi-node` pod runs on **every node group that schedules notebooks**, including
tainted CPU/GPU pools. Add tolerations through the add-on configuration if any pool is missing.

**3. File system networking.** The EFS file system must be in the cluster VPC, with a mount target
in every subnet that hosts notebook nodes, and a security group allowing NFS from the nodes:

```bash
aws efs describe-mount-targets --file-system-id <fs-id> --region <region>
aws efs create-mount-target --file-system-id <fs-id> --subnet-id <node-subnet-id> \
  --security-groups <efs-sg-id> --region <region>      # repeat per missing subnet
aws ec2 authorize-security-group-ingress --group-id <efs-sg-id> --protocol tcp --port 2049 \
  --source-group <node-sg-id> --region <region>
```

**4. StorageClass.** Apply [efs-storageclass.example.yaml](efs-storageclass.example.yaml) after
setting `fileSystemId`. It uses dynamic access points (`provisioningMode: efs-ap`), one directory
per claim. `uid`/`gid` pin the access-point identity to the notebook user (`1000`/`100`), so files
written by the copy pod and read by notebooks share one owner. StorageClass parameters are
immutable; to change them, create a new class and point `API_WORKSPACE_STORAGE_CLASS` at it.
EFS ignores the requested size, but the API still validates it, so keep
`API_WORKSPACE_STORAGE_SIZE` at or below any existing claim's size.

**5. Verify provisioning before enabling the feature.**

```bash
kubectl apply -f efs-test-pvc.example.yaml
kubectl get pvc efs-rwx-smoke-test -n default -w     # must reach Bound within seconds
kubectl delete -f efs-test-pvc.example.yaml
```

## Enable

All three must change together; the two flags must agree (see the config references above).

1. Both notebook templates in `sandbox-worker-notebook-templates` contain the `shared-workspace`
   volume policy, volume, and read-only mount, as in
   [`infra/worker/configmap.yaml`](../worker/configmap.yaml).
2. API ConfigMap: `WORKSPACE_ENABLED: "true"`, `API_WORKSPACE_STORAGE_CLASS: <rwx-class>`,
   `API_WORKSPACE_STORAGE_SIZE: <size>`, then restart the API.
3. Worker: `WORKSPACE_ENABLED=true`, then restart the worker.

```bash
kubectl patch cm api-config -n sandbox --type merge \
  -p '{"data":{"WORKSPACE_ENABLED":"true","API_WORKSPACE_STORAGE_CLASS":"<rwx-class>","API_WORKSPACE_STORAGE_SIZE":"10Gi"}}'
kubectl rollout restart deployment/sandbox-api -n sandbox
kubectl set env deployment/sandbox-worker -n sandbox -c worker WORKSPACE_ENABLED=true
```

Create one notebook, then check `kubectl get pvc workspace -n <profile-namespace>` is `Bound`
and `/home/jovyan/workspace` is mounted read-only in the notebook.

## Disable

Set `WORKSPACE_ENABLED=false` on both the API and the worker and restart both. Templates can keep
their `shared-workspace` entries; the worker strips them when disabled. Existing `workspace`
claims are not garbage-collected: delete `Pending` ones, and reclaim bound ones only after
confirming their data is no longer needed.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `workspace` PVC `Pending` with *"Waiting for a volume to be created … by the external provisioner"* | CSI controller not running, or missing IAM permissions. Check the add-on status and controller logs (`kubectl logs -n kube-system deploy/efs-csi-controller -c csi-provisioner`). |
| PVC `Bound`, notebook pod stuck `ContainerCreating` with mount timeouts | No mount target in the node's subnet/AZ, or port 2049 blocked between nodes and the EFS security group. |
| Mount fails only on some nodes | `efs-csi-node` DaemonSet not scheduled on that node group (taints). |
| API logs a `workspace PVC storageClassName …` mismatch | An existing `workspace` claim uses a different class or a smaller size than the API settings. |
| Notebooks fail with `required PVC shared-workspace (workspace) was not available` | Any of the above; the worker waited `externalPVCWaitTimeout` for the claim to bind. |
