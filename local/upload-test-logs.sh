#!/bin/bash

SOURCE_FILE="./local/test-logs6.json"
BUCKET="gs://pub-sub-event-gcs-test"

for i in $(seq 1 20); do
  echo "Uploading caleb-test-logs${i}.json..."
  gcloud storage cp "$SOURCE_FILE" "${BUCKET}/caleb-test-logs${i}.json"
done

echo "Done. Uploaded 20 files."
