#!/bin/bash

source .env

npx caprover deploy --caproverUrl $CAPROVER_DOMAIN --appToken $CAPROVER_APP_TOKEN --appName $CAPROVER_APP_NAME -b $CAPROVER_GIT_BRANCH_NAME
