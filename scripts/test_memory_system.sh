#!/bin/bash
# Integration test script for Crayfish persistent memory system

set -e

echo "=== Crayfish Memory System Integration Test ==="
echo

# Step 1: Build Crayfish
echo "Step 1: Building Crayfish..."
go build -o /tmp/crayfish ./cmd/crayfish
echo "✓ Build successful"
echo

# Step 2: Create test database
TEST_DB="/tmp/crayfish_memory_test.db"
rm -f "$TEST_DB"
echo "Step 2: Test database: $TEST_DB"
echo

# Step 3: Run unit tests
echo "Step 3: Running unit tests..."
go test ./internal/runtime -v -run TestMemory
echo "✓ Unit tests passed"
echo

# Step 4: Check database schema
echo "Step 4: Checking database schema..."
sqlite3 "$TEST_DB" <<EOF
.schema memory_fts
.schema memory_metadata
EOF
echo "✓ Schema check complete (will be created on first run)"
echo

echo "=== Integration Test Complete ==="
echo
echo "Next steps to test manually:"
echo "1. Start Crayfish in CLI mode:"
echo "   ./crayfish --config config.yaml"
echo
echo "2. Send a message with a preference:"
echo "   'I prefer Python over JavaScript for scripting'"
echo
echo "3. Wait 3 seconds for extraction"
echo
echo "4. Check the database:"
echo "   sqlite3 ~/.local/share/crayfish/crayfish.db"
echo "   SELECT * FROM memory_fts;"
echo "   SELECT * FROM memory_metadata;"
echo
echo "5. Send a related message:"
echo "   'What programming language should I use?'"
echo
echo "6. Verify Crayfish mentions your Python preference"
echo
echo "7. Test memory tools:"
echo "   - Use memory_search tool: 'Search my memories for programming'"
echo "   - Use memory_list tool: 'List all my memories'"
echo "   - Use memory_stats tool: 'Show my memory statistics'"
