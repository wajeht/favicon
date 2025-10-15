#!/bin/bash

source .env

# Load nvm
export NVM_DIR="$HOME/.nvm"
[ -s "$NVM_DIR/nvm.sh" ] && \. "$NVM_DIR/nvm.sh"

npx caprover deploy --caproverUrl $CAPROVER_DOMAIN --appToken $CAPROVER_APP_TOKEN --appName $CAPROVER_APP_NAME -b $CAPROVER_GIT_BRANCH_NAME
