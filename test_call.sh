#!/bin/bash

# Test script for Puffin Twilio Call API
# Usage: ./test_call.sh [phone_number] [name]

GATEWAY_URL="http://localhost:8092"
PHONE_NUMBER=${1:-"+14159681140"}
NAME=${2:-"Bill"}
ANNOUNCEMENT="Hello ${NAME}. This is a test call from Puffin. How are you doing today?"

echo "Triggering call to ${NAME} at ${PHONE_NUMBER}..."

curl -X POST "${GATEWAY_URL}/api/calls" \
     -H "Content-Type: application/json" \
     -d "{
           \"agent_name\": \"puffin\",
           \"target_phone\": \"${PHONE_NUMBER}\",
           \"target_name\": \"${NAME}\",
           \"announcement\": \"${ANNOUNCEMENT}\"
         }"

echo -e "\nDone."
