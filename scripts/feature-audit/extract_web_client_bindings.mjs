import fs from 'node:fs/promises';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '../..');
const requireFromRoot = createRequire(path.join(repoRoot, 'package.json'));

function loadTypeScript() {
  const localTs = path.join(repoRoot, 'webui/node_modules/typescript/lib/typescript.js');
  try {
    return requireFromRoot(localTs);
  } catch (err) {
    throw new Error(
      `typescript compiler API not found at ${localTs}; install WebUI dependencies before running feature-audit extraction`,
      { cause: err },
    );
  }
}

function nodeText(sourceFile, node) {
  return node.getText(sourceFile);
}

function propertyName(ts, sourceFile, name) {
  if (!name) return '';
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNumericLiteral(name)) {
    return name.text;
  }
  return nodeText(sourceFile, name);
}

function sourceLine(sourceFile, node) {
  return sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile)).line + 1;
}

function pathInfo(ts, sourceFile, expr) {
  if (!expr) {
    return { path: '', path_kind: 'dynamic', confidence: 'low', notes: 'missing first call argument' };
  }
  if (ts.isStringLiteral(expr) || ts.isNoSubstitutionTemplateLiteral(expr)) {
    return { path: expr.text, path_kind: 'static', confidence: 'high', notes: '' };
  }
  if (ts.isTemplateExpression(expr)) {
    return { path: nodeText(sourceFile, expr), path_kind: 'template', confidence: 'high', notes: 'template literal preserved as source text' };
  }
  return { path: nodeText(sourceFile, expr), path_kind: 'dynamic', confidence: 'medium', notes: 'computed path preserved as source text' };
}

function methodFromOptions(ts, sourceFile, options) {
  if (!options || !ts.isObjectLiteralExpression(options)) return 'GET';
  for (const prop of options.properties) {
    if (!ts.isPropertyAssignment(prop)) continue;
    if (propertyName(ts, sourceFile, prop.name) !== 'method') continue;
    const init = prop.initializer;
    if (ts.isStringLiteral(init) || ts.isNoSubstitutionTemplateLiteral(init)) return init.text.toUpperCase();
    return nodeText(sourceFile, init);
  }
  return 'GET';
}

function findLevaraObject(ts, sourceFile) {
  for (const stmt of sourceFile.statements) {
    if (!ts.isVariableStatement(stmt)) continue;
    for (const decl of stmt.declarationList.declarations) {
      if (!ts.isIdentifier(decl.name) || decl.name.text !== 'levara') continue;
      if (decl.initializer && ts.isObjectLiteralExpression(decl.initializer)) return decl.initializer;
    }
  }
  throw new Error('could not find `export const levara = { ... }` object in API client source');
}

function collectCalls(ts, sourceFile, client, rootNode) {
  const rows = [];

  function visit(node) {
    if (ts.isCallExpression(node)) {
      const callee = node.expression;
      const calleeName = ts.isIdentifier(callee) ? callee.text : '';
      if (calleeName === 'api' || calleeName === 'fetch') {
        const info = pathInfo(ts, sourceFile, node.arguments[0]);
        rows.push({
          client,
          call_index: rows.length + 1,
          method: methodFromOptions(ts, sourceFile, node.arguments[1]),
          path: info.path,
          path_kind: info.path_kind,
          source_line: sourceLine(sourceFile, node),
          confidence: info.confidence,
          notes: calleeName === 'fetch'
            ? [info.notes, 'direct fetch call'].filter(Boolean).join('; ')
            : info.notes,
        });
      }
    }
    ts.forEachChild(node, visit);
  }

  visit(rootNode);
  return rows;
}

export function extractWebClientBindings(sourceText, fileName = 'webui/src/lib/api.ts') {
  const ts = loadTypeScript();
  const sourceFile = ts.createSourceFile(fileName, sourceText, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const levaraObject = findLevaraObject(ts, sourceFile);
  const rows = [];

  for (const prop of levaraObject.properties) {
    if (!ts.isPropertyAssignment(prop) && !ts.isShorthandPropertyAssignment(prop) && !ts.isMethodDeclaration(prop)) {
      continue;
    }
    const client = propertyName(ts, sourceFile, prop.name);
    if (!client) continue;

    let target = null;
    if (ts.isPropertyAssignment(prop)) target = prop.initializer;
    if (ts.isMethodDeclaration(prop)) target = prop.body;
    if (!target) continue;

    rows.push(...collectCalls(ts, sourceFile, client, target));
  }

  return rows;
}

async function main() {
  const input = process.argv[2] || path.join(repoRoot, 'webui/src/lib/api.ts');
  const source = await fs.readFile(path.resolve(input), 'utf8');
  const rows = extractWebClientBindings(source, input);
  process.stdout.write(`${JSON.stringify(rows, null, 2)}\n`);
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}
