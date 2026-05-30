const Long = require('long');

function inquire(moduleName) {
  if (moduleName === 'long') return Long;
  return null;
}

module.exports = inquire;
module.exports.default = inquire;
