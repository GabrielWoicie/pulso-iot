// Optional verification fixture. Production backend needs Go/PostgreSQL/EMQX, never Node/PGlite.
import { PGlite } from '@electric-sql/pglite';
import { PGLiteSocketServer } from '@electric-sql/pglite-socket';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const db = await PGlite.create();
const server = new PGLiteSocketServer({ db, host: '127.0.0.1', port: 55432, maxConnections: 1 });
let code = 1;
try {
  await server.start();
  console.log((await db.query('select version()')).rows[0].version);
  const root = fileURLToPath(new URL('../../', import.meta.url));
  // The fixture is single-connection. Native PostgreSQL tests use the normal pool.
  const child = spawn(process.env.GO_BINARY || 'go', ['test', '-race', '-tags', 'integration', '-count=1', '-v', './...'], {
    cwd: root,
    stdio: 'inherit',
    env: { ...process.env, PULSO_TEST_DATABASE_URL: 'postgres://postgres:postgres@127.0.0.1:55432/postgres?sslmode=disable&default_query_exec_mode=exec&pool_max_conns=1' },
  });
  code = await new Promise((resolve, reject) => { child.on('error', reject); child.on('exit', c => resolve(c ?? 1)); });
} finally { await server.stop(); await db.close(); }
process.exitCode = code;
