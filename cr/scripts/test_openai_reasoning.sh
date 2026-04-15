#!/bin/bash
# Test script to probe GPT-5.3-Codex reasoning summaries and caching
# Usage: OPENAI_API_KEY=sk-... ./cr/scripts/test_openai_reasoning.sh

set -e

if [ -z "$OPENAI_API_KEY" ]; then
  echo "Error: Set OPENAI_API_KEY environment variable"
  exit 1
fi

API_URL="https://api.openai.com/v1/responses"
MODEL="gpt-5.3-codex"

echo "=== Test 1: Reasoning summaries with effort=high, summary=auto ==="
echo ""

RESPONSE=$(curl -s "$API_URL" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "'"$MODEL"'",
    "instructions": "You are a helpful assistant.",
    "input": "What is 25 * 37? Show your work step by step.",
    "reasoning": {
      "effort": "high",
      "summary": "auto"
    },
    "include": ["reasoning.encrypted_content"],
    "store": false,
    "stream": false
  }')

echo "Full response:"
echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"
echo ""

# Extract key fields
echo "=== Analysis ==="
echo ""
echo "Output items:"
echo "$RESPONSE" | python3 -c "
import json, sys
r = json.load(sys.stdin)
if 'output' in r:
    for i, item in enumerate(r['output']):
        print(f'  [{i}] type={item[\"type\"]}')
        if item['type'] == 'reasoning':
            if 'summary' in item and item['summary']:
                print(f'       summary: {item[\"summary\"]}')
            else:
                print(f'       summary: NONE/EMPTY')
            if 'encrypted_content' in item:
                print(f'       encrypted_content: {item[\"encrypted_content\"][:60]}...')
        elif item['type'] == 'message':
            for c in item.get('content', []):
                print(f'       content: {c.get(\"text\", \"\")[:100]}')
if 'usage' in r:
    u = r['usage']
    print()
    print('Usage:')
    print(f'  input_tokens: {u.get(\"input_tokens\", 0)}')
    print(f'  output_tokens: {u.get(\"output_tokens\", 0)}')
    details = u.get('output_tokens_details', {})
    if details:
        print(f'  reasoning_tokens: {details.get(\"reasoning_tokens\", 0)}')
    cache_details = u.get('input_tokens_details', {})
    if cache_details:
        print(f'  cached_tokens: {cache_details.get(\"cached_tokens\", 0)}')
    print()
    # Cost calculation
    input_cost = u.get('input_tokens', 0) * 1.75 / 1_000_000
    output_cost = u.get('output_tokens', 0) * 14.0 / 1_000_000
    cached = cache_details.get('cached_tokens', 0) if cache_details else 0
    cache_savings = cached * (1.75 - 0.175) / 1_000_000
    print(f'  Estimated cost: \${input_cost + output_cost:.6f} (input: \${input_cost:.6f}, output: \${output_cost:.6f})')
    if cached > 0:
        print(f'  Cache savings: \${cache_savings:.6f} ({cached} cached tokens)')
else:
    print('No usage data in response')
    if 'error' in r:
        print(f'Error: {r[\"error\"]}')
" 2>/dev/null

echo ""
echo "=== Test 2: Streaming mode - check for reasoning delta events ==="
echo ""

curl -s "$API_URL" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "'"$MODEL"'",
    "instructions": "You are a helpful assistant.",
    "input": "What is the square root of 144?",
    "reasoning": {
      "effort": "high",
      "summary": "auto"
    },
    "include": ["reasoning.encrypted_content"],
    "store": false,
    "stream": true
  }' | while IFS= read -r line; do
    # Only show lines with reasoning-related events
    if echo "$line" | grep -q "reasoning\|response.completed\|response.output_item.added\|response.output_item.done"; then
      echo "$line"
    fi
  done

echo ""
echo "=== Test 3: Same request again (check caching) ==="
echo ""

RESPONSE2=$(curl -s "$API_URL" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "'"$MODEL"'",
    "instructions": "You are a helpful assistant.",
    "input": "What is 25 * 37? Show your work step by step.",
    "reasoning": {
      "effort": "high",
      "summary": "auto"
    },
    "include": ["reasoning.encrypted_content"],
    "store": false,
    "stream": false
  }')

echo "$RESPONSE2" | python3 -c "
import json, sys
r = json.load(sys.stdin)
if 'usage' in r:
    u = r['usage']
    print('Usage (second request - check for caching):')
    print(f'  input_tokens: {u.get(\"input_tokens\", 0)}')
    print(f'  output_tokens: {u.get(\"output_tokens\", 0)}')
    details = u.get('output_tokens_details', {})
    if details:
        print(f'  reasoning_tokens: {details.get(\"reasoning_tokens\", 0)}')
    cache_details = u.get('input_tokens_details', {})
    if cache_details:
        print(f'  cached_tokens: {cache_details.get(\"cached_tokens\", 0)}')
    else:
        print('  cached_tokens: field not present')
    print()
    print('Full usage object:')
    print(json.dumps(u, indent=2))
" 2>/dev/null

echo ""
echo "=== Test 4: Try summary=detailed instead of auto ==="
echo ""

RESPONSE3=$(curl -s "$API_URL" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "'"$MODEL"'",
    "instructions": "You are a helpful assistant.",
    "input": "Explain why the sky is blue in one paragraph.",
    "reasoning": {
      "effort": "high",
      "summary": "detailed"
    },
    "include": ["reasoning.encrypted_content"],
    "store": false,
    "stream": false
  }')

echo "$RESPONSE3" | python3 -c "
import json, sys
r = json.load(sys.stdin)
if 'output' in r:
    for i, item in enumerate(r['output']):
        if item['type'] == 'reasoning':
            print(f'Reasoning item:')
            if 'summary' in item and item['summary']:
                print(f'  SUMMARY FOUND: {json.dumps(item[\"summary\"], indent=2)}')
            else:
                print(f'  No summary field (or empty)')
            keys = list(item.keys())
            print(f'  Keys present: {keys}')
if 'error' in r:
    print(f'Error: {json.dumps(r[\"error\"], indent=2)}')
" 2>/dev/null

echo ""
echo "Done."
