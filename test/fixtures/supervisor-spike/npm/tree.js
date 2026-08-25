const { spawn } = require('node:child_process')
const path = require('node:path')

const child = spawn(process.execPath, [path.join(__dirname, 'worker.js')], {
  stdio: 'ignore',
  windowsHide: true,
})

child.unref()
setInterval(() => {}, 60_000)
