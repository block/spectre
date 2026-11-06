#!/bin/sh
# Run demo/mirror-pod.yaml in a kind cluster and check that the proxies mirror
# across containers while the candidate's direct IP traffic is refused.
set -eu

image="$1"
cluster=spectre-mirror-pod
source_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
export KUBECONFIG="$work/kubeconfig"

# shellcheck disable=SC2329 # Invoked indirectly by trap.
cleanup() {
	status=$?
	if [ "$status" -ne 0 ] && [ -f "$KUBECONFIG" ]; then
		kubectl describe pod spectre-mirror >&2 || true
		for container in spectre-init server forecasts proxy reference candidate; do
			printf '\n--- %s logs ---\n' "$container" >&2
			kubectl logs spectre-mirror -c "$container" >&2 || true
		done
	fi
	kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT HUP INT TERM

kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --wait 120s
kind load docker-image "$image" --name "$cluster"

sed "s|image: IMAGE|image: $image|" "$source_root/demo/mirror-pod.yaml" | kubectl apply -f -
kubectl wait --for=condition=Ready pod/spectre-mirror --timeout=180s

# exec_in runs a command in one of the pod's containers, as that container's user.
exec_in() {
	container=$1
	shift
	kubectl exec spectre-mirror -c "$container" -- "$@"
}

# count_log counts the JSON records a container logged that match a jq filter.
count_log() {
	kubectl logs spectre-mirror -c "$1" | jq -cR "fromjson? | select($2)" | jq -s length
}

wait_for_log() {
	attempt=0
	while [ "$attempt" -lt 100 ]; do
		if [ "$(count_log "$1" "$2")" -ge 1 ]; then
			return
		fi
		attempt=$((attempt + 1))
		sleep 0.2
	done
	printf 'timed out waiting for %s to log %s\n' "$1" "$2" >&2
	exit 1
}

# The reference may open IP connections, so clients reach ingress from it.
if ! exec_in reference wget -q -T 5 -O - 'http://127.0.0.1:50050/v2/forecast?location=london' | grep -Fq '"alerts"'; then
	printf 'ingress did not return the reference response\n' >&2
	exit 1
fi
wait_for_log candidate '.path == "/v2/forecast"'
wait_for_log proxy '.msg == "Response comparison completed" and .route == "GET /v2/forecast" and .outcome == "equivalent"'

# The candidate's forecast call crossed its egress socket and was answered with
# the reference's, so the provider served it once.
wait_for_log proxy '.msg == "Candidate request matched a reference request" and .route == "GET forecasts.example/v1/forecasts/{location}"'
provider_calls=$(count_log forecasts '.path == "/v1/forecasts/london"')
if [ "$provider_calls" -ne 1 ]; then
	printf 'forecast provider received %s requests, expected 1\n' "$provider_calls" >&2
	exit 1
fi

# The candidate cannot reach the proxies' listeners, or anything else, over IP.
if exec_in candidate wget -q -T 2 -O /dev/null http://127.0.0.1:50063/livez 2>/dev/null; then
	printf 'candidate reached a loopback listener over IP\n' >&2
	exit 1
fi
if exec_in candidate wget -q -T 2 -O /dev/null http://kubernetes.default.svc/ 2>/dev/null; then
	printf 'candidate reached the cluster network over IP\n' >&2
	exit 1
fi
exec_in reference wget -q -T 2 -O /dev/null http://127.0.0.1:50063/livez

printf 'Mirror pod test passed\n'
