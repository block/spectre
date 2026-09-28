setup! : mkdir -p dist/sockets
reference **/*.go !**/*_test.go after=setup: spectre-sample --listen="unix:$(pwd)/dist/sockets/reference.sock"
candidate **/*.go !**/*_test.go after=setup: spectre-sample --listen="unix:$(pwd)/dist/sockets/candidate.sock"
reference-ready! after=reference: until curl -fsS -o /dev/null --unix-socket dist/sockets/reference.sock http://localhost/readyz; do sleep 0.2; done
candidate-ready! after=candidate: until curl -fsS -o /dev/null --unix-socket dist/sockets/candidate.sock http://localhost/readyz; do sleep 0.2; done
ingress **/*.go !**/*_test.go internal/sample/comparison.js after=reference-ready,candidate-ready: spectre-ingress --listen="unix:$(pwd)/dist/sockets/ingress.sock" --reference="h2c+unix:$(pwd)/dist/sockets/reference.sock" --candidate="h2c+unix:$(pwd)/dist/sockets/candidate.sock" --comparison-script=internal/sample/comparison.js --log-level=debug
ingress-ready! after=ingress: until curl -fsS -o /dev/null --unix-socket dist/sockets/ingress.sock http://localhost/readyz; do sleep 0.2; done
