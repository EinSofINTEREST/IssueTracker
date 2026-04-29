#!/usr/bin/env bash
#
# pr-resolve-comments.sh
#
# 처리 완료한 PR review comment 들에 👀 reaction 일괄 추가 + 해당 thread resolve.
# .claude/loop.md 의 "처리 방식" 절차를 단축하기 위한 helper (이슈 #152).
#
# Usage:
#   scripts/pr-resolve-comments.sh <PR번호> <comment_id1> [<comment_id2> ...]
#
# Example:
#   scripts/pr-resolve-comments.sh 153 3160753464 3160753479 3160762665
#
# 동작:
#   1. 각 comment 에 'eyes' reaction 추가 (REST API)
#   2. comment_id → review thread_id 매핑 후 resolveReviewThread 호출 (GraphQL)
#
# 사전 조건:
#   - gh CLI 가 인증되어 있어야 함 (gh auth status)
#   - PR 이 위치한 repo 의 owner/name 은 gh repo view 로 자동 추출

set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "Usage: $0 <PR번호> <comment_id1> [<comment_id2> ...]" >&2
  exit 1
fi

pr=$1
shift

owner_repo=$(gh repo view --json owner,name -q '"\(.owner.login)/\(.name)"')
owner=${owner_repo%/*}
name=${owner_repo#*/}

# 1) eyes reaction 일괄 추가
for id in "$@"; do
  gh api -X POST "repos/${owner_repo}/pulls/comments/${id}/reactions" \
    -f content="eyes" --silent
done

# 2) comment_id 들을 jq IN() 매칭용 quoted 리스트로 변환
ids_quoted=$(printf '"%s",' "$@" | sed 's/,$//')

# 3) review thread 전수 조회 → 우리 comment_id 가 첫 댓글인 thread 들의 thread_id 추출
thread_ids=$(gh api graphql -f query="
{
  repository(owner: \"${owner}\", name: \"${name}\") {
    pullRequest(number: ${pr}) {
      reviewThreads(first: 100) {
        nodes {
          id
          comments(first: 1) {
            nodes { databaseId }
          }
        }
      }
    }
  }
}" --jq ".data.repository.pullRequest.reviewThreads.nodes[]
         | select(.comments.nodes[0].databaseId | tostring | IN(${ids_quoted}))
         | .id")

# 4) 각 thread resolve
resolved=0
while IFS= read -r tid; do
  [[ -z "$tid" ]] && continue
  gh api graphql \
    -f query='mutation($id: ID!) {
      resolveReviewThread(input: {threadId: $id}) {
        thread { isResolved }
      }
    }' \
    -F id="$tid" --silent
  resolved=$((resolved + 1))
done <<<"$thread_ids"

echo "PR #${pr}: ${#}개 comment 에 eyes reaction + ${resolved}개 thread resolve 완료"
