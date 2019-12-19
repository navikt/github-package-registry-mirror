FROM node:12-slim

WORKDIR /app

ADD . .

RUN npm ci

CMD ["node", "index.js"]
