import { createTheme } from '@mui/material/styles'

export const portfolioTheme = createTheme({
  palette: {
    mode: 'dark',
    primary: {
      main: '#52c7b8',
      light: '#8be6d7',
      dark: '#164d43',
      contrastText: '#071312',
    },
    background: {
      default: '#111817',
      paper: '#1a2524',
    },
    text: {
      primary: '#edf6f5',
      secondary: '#a9c1bd',
    },
    divider: '#31514e',
    error: { main: '#ff8f9d' },
    warning: { main: '#ffdc9a' },
  },
  typography: {
    fontFamily: 'Inter, ui-sans-serif, system-ui, sans-serif',
    h1: { fontWeight: 800, letterSpacing: '-.04em' },
    h2: { fontWeight: 700, letterSpacing: '-.02em' },
    h3: { fontWeight: 700 },
    button: { textTransform: 'none', fontWeight: 700 },
  },
  shape: { borderRadius: 10 },
  components: {
    MuiCssBaseline: {
      styleOverrides: {
        body: { minWidth: 320 },
        '*': { boxSizing: 'border-box' },
      },
    },
    MuiAppBar: {
      defaultProps: { elevation: 0 },
      styleOverrides: {
        root: { backgroundImage: 'none', borderBottom: '1px solid #31514e' },
      },
    },
    MuiToolbar: { styleOverrides: { dense: { minHeight: 44 } } },
    MuiPaper: {
      styleOverrides: {
        root: { backgroundImage: 'none' },
      },
    },
    MuiTableCell: {
      styleOverrides: {
        root: { borderColor: '#2d4441' },
        head: { backgroundColor: '#1a2524', fontWeight: 700 },
      },
    },
    MuiButton: { defaultProps: { disableElevation: true } },
  },
})
