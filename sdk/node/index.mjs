// ESM entry: the same single instance as index.cjs.
import sdk from './index.cjs';

export const { version, init, captureException, captureMessage, addBreadcrumb, setTag, setTags, flush } = sdk;
export default sdk;
