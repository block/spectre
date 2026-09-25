#!/bin/sh

set -eu

service=spectre.sample.v1.UserService
ingress_address=127.0.0.1:55150

wait_for_log() {
	process=$1
	message=$2
	attempt=0
	while [ "$attempt" -lt 100 ]; do
		if grep -F "$process" "$SPECTRE_PROCTOR_LOG" | grep -Fq "$message"; then
			return
		fi
		attempt=$((attempt + 1))
		sleep 0.1
	done
	printf 'timed out waiting for %s to log %s\n' "$process" "$message" >&2
	exit 1
}

wait_for_log_count() {
	process=$1
	message=$2
	expected=$3
	attempt=0
	while [ "$attempt" -lt 100 ]; do
		count=$(grep -F "$process" "$SPECTRE_PROCTOR_LOG" | grep -Fc "$message" || true)
		if [ "$count" -ge "$expected" ]; then
			return
		fi
		attempt=$((attempt + 1))
		sleep 0.1
	done
	printf 'timed out waiting for %s to log %s %s times\n' "$process" "$message" "$expected" >&2
	exit 1
}

verify() {
	equal_response=$(grpcurl -plaintext -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers")
	if ! printf '%s\n' "$equal_response" | grep -Fq 'Alex Example'; then
		printf 'equivalent request did not return the reference response\n' >&2
		exit 1
	fi
	wait_for_log candidate '"path":"/spectre.sample.v1.UserService/ListUsers"'
	wait_for_log ingress '"msg":"Response comparator completed","kind":"field","target":"spectre.sample.v1.User.roles"'
	wait_for_log ingress '"target":"spectre.sample.v1.ListUsersResponse.generated_at","response_path":"$.generatedAt","matched":true'
	wait_for_log ingress '"msg":"Response comparison completed","path":"/spectre.sample.v1.UserService/ListUsers","outcome":"equivalent"'
	# The comparison timeout has elapsed before this probe, so a second mirror proves
	# the reordered roles did not quarantine the candidate.
	sleep 1.1
	grpcurl -plaintext -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers" >/dev/null
	wait_for_log_count candidate '"path":"/spectre.sample.v1.UserService/ListUsers"' 2

	different_response=$(grpcurl -plaintext -connect-timeout 2 -max-time 5 \
		-d '{"id":"user-2"}' "$ingress_address" "$service/GetUser")
	if ! printf '%s\n' "$different_response" | grep -Fq 'Sam Sample'; then
		printf 'divergent request did not return the reference response\n' >&2
		exit 1
	fi
	wait_for_log candidate '"path":"/spectre.sample.v1.UserService/GetUser"'
	wait_for_log ingress '"msg":"Response comparison completed","path":"/spectre.sample.v1.UserService/GetUser","outcome":"divergent"'
	wait_for_log ingress '"msg":"Candidate quarantined"'

	candidate_list_calls=$(grep -F candidate "$SPECTRE_PROCTOR_LOG" | \
		grep -Fc '"path":"/spectre.sample.v1.UserService/ListUsers"')
	grpcurl -plaintext -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers" >/dev/null
	sleep 0.2
	candidate_list_calls_after=$(grep -F candidate "$SPECTRE_PROCTOR_LOG" | \
		grep -Fc '"path":"/spectre.sample.v1.UserService/ListUsers"')
	if [ "$candidate_list_calls_after" -ne "$candidate_list_calls" ]; then
		printf 'candidate received a request after response divergence\n' >&2
		exit 1
	fi

	: > "$SPECTRE_PROCTOR_RESULT"
}

if [ "${1:-}" = verify ]; then
	verify
	exit 0
fi

source_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
test_root=$(mktemp -d)
proctor_pid=
# shellcheck disable=SC2329 # Invoked indirectly by trap.
cleanup() {
	if [ -n "$proctor_pid" ] && kill -0 "$proctor_pid" 2>/dev/null; then
		kill -TERM "$proctor_pid" 2>/dev/null || true
		wait "$proctor_pid" 2>/dev/null || true
	fi
	rm -rf "$test_root"
}
trap cleanup EXIT HUP INT TERM

sed -e 's/"ROLE_ADMIN", "ROLE_EDITOR"/"ROLE_EDITOR", "ROLE_ADMIN"/' \
	-e 's/"team": "platform"/"team": "candidate"/' \
	"$source_root/internal/sample/testdata/users.json" > "$test_root/candidate.json"
export SPECTRE_CANDIDATE_DATA="$test_root/candidate.json"
export SPECTRE_COMPARISON_SCRIPT="$source_root/internal/sample/comparison.js"
export SPECTRE_PROCTOR_LOG="$test_root/proctor.log"
export SPECTRE_PROCTOR_RESULT="$test_root/passed"

cd "$source_root"
proctor "$source_root/Procfile.integration" > "$SPECTRE_PROCTOR_LOG" 2>&1 &
proctor_pid=$!

attempt=0
while [ "$attempt" -lt 600 ]; do
	if [ -f "$SPECTRE_PROCTOR_RESULT" ]; then
		kill -TERM "$proctor_pid" 2>/dev/null || true
		wait "$proctor_pid" 2>/dev/null || true
		proctor_pid=
		printf 'Response comparison integration test passed\n'
		exit 0
	fi
	if ! kill -0 "$proctor_pid" 2>/dev/null; then
		wait "$proctor_pid" 2>/dev/null || true
		proctor_pid=
		cat "$SPECTRE_PROCTOR_LOG" >&2
		exit 1
	fi
	attempt=$((attempt + 1))
	sleep 0.1
done

printf 'response comparison integration test timed out\n' >&2
cat "$SPECTRE_PROCTOR_LOG" >&2
exit 1
