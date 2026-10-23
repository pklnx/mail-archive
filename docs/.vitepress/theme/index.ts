// The pklnx VitePress theme (pklnx/design-system v0.2.0): the default theme
// without its Inter font, then the pklnx tokens and IBM Plex, then our CSS.
import DefaultTheme from "vitepress/theme-without-fonts";
import "@pklnx/design-system/theme/vitepress.css";
import "./custom.css";

export default DefaultTheme;
