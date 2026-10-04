#!/usr/bin/env bash
# Generate TypeScript types from OpenAPI spec
# Usage: ./scripts/generate-types.sh

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OPENAPI_SPEC="${ROOT_DIR}/docs/api/openapi.yaml"
OUTPUT_DIR="${ROOT_DIR}/web/src/lib/api-generated"
TEMP_DIR="$(mktemp -d)"

echo "🔧 Generating TypeScript types from OpenAPI spec..."

# Check if openapi-generator is available
if ! command -v openapi-generator-cli &> /dev/null; then
    echo "⚠️  openapi-generator-cli not found. Installing via npx..."
    npx @openapitools/openapi-generator-cli version 2>/dev/null || {
        echo "📦 Installing @openapitools/openapi-generator-cli globally..."
        npm install -g @openapitools/openapi-generator-cli
    }
fi

# Clean output directory
rm -rf "${OUTPUT_DIR}"
mkdir -p "${OUTPUT_DIR}"

# Generate TypeScript types
echo "📝 Generating types..."
npx @openapitools/openapi-generator-cli generate \
    -i "${OPENAPI_SPEC}" \
    -g typescript-axios \
    -o "${TEMP_DIR}/generated" \
    --additional-properties=supportsES6=true,withInterfaces=true,typescriptThreePlus=true,generateEnums=true,enumPropertyNaming=camelCase,modelPropertyNaming=camelCase

# Copy generated types to our lib
echo "📋 Copying generated types..."
cp -r "${TEMP_DIR}/generated/src/*" "${OUTPUT_DIR}/"

# Create index.ts that re-exports everything
cat > "${OUTPUT_DIR}/index.ts" << 'EOF'
// Auto-generated from docs/api/openapi.yaml
// DO NOT EDIT MANUALLY - run scripts/generate-types.sh instead

export * from './models';
export * from './api';
export * from './apis';
EOF

# Clean up temp directory
rm -rf "${TEMP_DIR}"

echo "✅ Type generation complete!"
echo "📁 Generated types in: ${OUTPUT_DIR}"
echo ""
echo "To use in your code:"
echo "  import { Shipment, Alert, ... } from '@/lib/api-generated';"