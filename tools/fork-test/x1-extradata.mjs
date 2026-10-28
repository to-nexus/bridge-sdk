// helper for x1-executor.sh: builds the X1 extraData with the SDK (js/dist) - prints it as one hex string.
//   node x1-extradata.mjs <router> <usdtx> <oneusd> <fee> <recipient> <deadline> <amountIn> <amountOutMinimum>
import { encodeX1ExtraData } from '../../js/dist/index.js';
const [router, tokenIn, tokenOut, fee, recipient, deadline, amountIn, amountOutMinimum] = process.argv.slice(2);
console.log(encodeX1ExtraData({ router, tokenIn, tokenOut, fee: Number(fee), recipient, deadline: BigInt(deadline), amountIn: BigInt(amountIn), amountOutMinimum: BigInt(amountOutMinimum) }));
