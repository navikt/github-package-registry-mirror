FROM node:24-slim

WORKDIR /app

ADD . .

RUN npm ci

CMD ["node", "src/index.js"]
