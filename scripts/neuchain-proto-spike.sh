#!/usr/bin/env bash
# NeuChain proto spike bootstrap (adr-002). Does the mechanical part of the
# time-boxed spike so the engineer can focus on the judgement call:
#
#   1. get NeuChain's .proto files + record the exact upstream commit
#   2. generate Go stubs into pkg/adapters/neuchain/proto/
#   3. print what to inspect next (tx format, signing, epoch/batch semantics)
#
# Usage:
#   scripts/neuchain-proto-spike.sh              # clone shallow, extract, gen
#   scripts/neuchain-proto-spike.sh /path/to/NeuChain   # use a local checkout
#
# Requires: git, protoc, protoc-gen-go, protoc-gen-go-grpc
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="$ROOT/pkg/adapters/neuchain/proto"
REF="${NEUCHAIN_REF:-ev}"
REPO="${NEUCHAIN_REPO:-https://github.com/iDC-NEU/NeuChain.git}"

for t in git protoc; do command -v "$t" >/dev/null || { echo "missing $t"; exit 1; }; done
command -v protoc-gen-go     >/dev/null || { echo "need protoc-gen-go (go install google.golang.org/protobuf/cmd/protoc-gen-go@latest)"; exit 1; }
command -v protoc-gen-go-grpc>/dev/null || { echo "need protoc-gen-go-grpc (go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest)"; exit 1; }

SRC="${1:-}"
TMP=""
if [ -z "$SRC" ]; then
  TMP="$(mktemp -d)"; SRC="$TMP/NeuChain"
  echo "cloning $REPO @ $REF (shallow)"
  git clone --branch "$REF" --depth 1 "$REPO" "$SRC"
fi
COMMIT="$(git -C "$SRC" rev-parse HEAD)"
echo "NeuChain commit: $COMMIT"

mapfile -t PROTOS < <(cd "$SRC" && find . -name '*.proto' | sort)
[ "${#PROTOS[@]}" -gt 0 ] || { echo "no .proto files found in $SRC"; exit 1; }
echo "found ${#PROTOS[@]} proto file(s):"; printf '  %s\n' "${PROTOS[@]}"

mkdir -p "$DEST"
for p in "${PROTOS[@]}"; do
  mkdir -p "$DEST/$(dirname "$p")"
  cp "$SRC/$p" "$DEST/$p"
done

cat > "$DEST/ORIGIN" <<EOF
repo:   $REPO
ref:    $REF
commit: $COMMIT
pulled: $(date -u +%FT%TZ)
files:
$(printf '  %s\n' "${PROTOS[@]}")

regenerate:
  protoc -I pkg/adapters/neuchain/proto \\
    --go_out=pkg/adapters/neuchain/proto --go_opt=paths=source_relative \\
    --go-grpc_out=pkg/adapters/neuchain/proto --go-grpc_opt=paths=source_relative \\
    \$(cd pkg/adapters/neuchain/proto && find . -name '*.proto')
EOF

echo "generating Go stubs -> $DEST"
( cd "$DEST" && protoc -I . \
    --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    $(find . -name '*.proto') ) || {
  echo
  echo "protoc failed - common causes:"
  echo "  * proto files import each other with paths relative to a different root"
  echo "    (add more -I include dirs)"
  echo "  * missing 'option go_package' - add it or pass --go_opt=Mfile.proto=..."
  exit 1
}

[ -n "$TMP" ] && rm -rf "$TMP"

cat <<EOF

stubs generated. Next (the actual spike - record findings in
docs/platforms/neuchain-client-implementation.md):

  1. Which RPC submits a transaction? What message does it take?
  2. How is a transaction constructed - fields, encoding, ordering?
  3. Signing: algorithm, what bytes are signed, signature encoding? Is it even
     per-transaction, or per-batch / per-client-session?
  4. Epoch / batch: does the client pick an epoch, or does the server assign it?
  5. Finality: is there a stream of committed blocks, or must the client poll
     block height?

If 2-4 port to Go cleanly  -> pure-Go adapter (txbuild.go, sign.go, grpc_client.go)
If not                     -> wrap the native 'user' binary (grpc_client.go execs it)

Either way fill in neuchain-client-implementation.md sections 1-7.
EOF
