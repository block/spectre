# SPECTRE mirror pod demo

[`mirror-pod.yaml`](mirror-pod.yaml) lays out a mirror the way it runs in
Kubernetes: the reference, the candidate, `spectre proxy`, `spectre serve` and a
sample dependency each run in their own container, and a `spectre contain` init
container rejects the candidate's IP traffic. The containers share unix sockets
through two volumes, and the candidate sees only its own.

The services are two copies of the builtin sample backend, standing in for a
reference and a candidate. `demo/Dockerfile` builds the one image every
container runs, with the binaries and the sample's comparison config.

## Run it

```unknown
$ bit test-mirror-pod
```

This builds the image, runs the pod in a local kind cluster with Docker, and
checks that it mirrors, replays egress and refuses the candidate's direct IP
traffic. It deletes the cluster when it finishes.
