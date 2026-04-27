// @ts-nocheck
'use strict';
const { createApp } = require('./app');
const { createGCSStorage, createLocalStorage } = require('./storage');

const storage = process.env.STORAGE_BACKEND === 'local'
    ? createLocalStorage(process.env.STORAGE_PATH || './storage')
    : createGCSStorage('github-package-registry-storage');

const app = createApp({ storageFn: storage });
const port = 8080;
app.listen(port, () => console.log(`App listening on port ${port}`));

['SIGINT', 'SIGTERM'].forEach(signal => {
    process.once(signal, function () {
        console.log(`${signal} received, exiting.`);
        process.exit(0);
    });
});
